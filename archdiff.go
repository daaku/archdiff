// Command archdiff implements a tool to view and manipulate a "system
// level diff" of sorts. It's somewhat akin to the "things that differ"
// if a new system was given the exact current set of packages
// combined with a target directory that can be considered an "overlay"
// on top of the packages for things like configuration and or ignored
// data.
package main

import (
	"bytes"
	"crypto/md5"
	"flag"
	"fmt"
	"github.com/daaku/go-alpm"
	"github.com/daaku/go.copyfile"
	"github.com/daaku/go.flagconfig"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type File struct {
	Name string
	Hash string
}

type ArchDiff struct {
	Verbose    bool
	Silent     bool
	DryRun     bool
	Root       string
	DB         string
	Repo       string
	IgnoreFile string
	MaxProcs   int

	ignoreGlob         []string
	backupFile         []File
	modifiedBackupFile []File
	localDb            *alpm.Db
	alpmHandle         *alpm.Handle
	allPackageFile     []File
	allFile            []File
	unpackagedFile     []File
	repoFile           []File
	diffRepoFile       []File
	missingInRepo      []File
}

var cp = &copyfile.Copy{
	KeepLinks: true,
	Force:     true,
	Clobber:   true,
}

func filehash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := md5.New()
	io.Copy(h, file)
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func contains(name string, list []File) bool {
	for _, file := range list {
		if file.Name == name {
			return true
		}
	}
	return false
}

func (ad *ArchDiff) IgnoreGlob() []string {
	if ad.ignoreGlob == nil && ad.IgnoreFile != "" {
		content, err := ioutil.ReadFile(ad.IgnoreFile)
		if err != nil {
			log.Fatalf("failed to read ignore file %s: %s", ad.IgnoreFile, err)
		}
		lines := bytes.Split(content, []byte{'\n'})
		for _, r := range lines {
			l := string(r)
			if len(l) == 0 {
				continue
			}
			if l[0] == '#' {
				continue
			}
			ad.ignoreGlob = append(ad.ignoreGlob, l)
		}
	}
	return ad.ignoreGlob
}

func (ad *ArchDiff) IsIgnored(path string) bool {
	for _, glob := range ad.IgnoreGlob() {
		matched, err := filepath.Match(glob, path)
		if err != nil {
			log.Fatalf("Match error: %s", err)
		}
		if matched {
			return true
		}
	}
	return false
}

func (ad *ArchDiff) Alpm() *alpm.Handle {
	if ad.alpmHandle == nil {
		var err error
		ad.alpmHandle, err = alpm.Init(ad.Root, ad.DB)
		if err != nil {
			log.Fatalf("Failed to initialize pacman: %s", err)
		}
	}
	return ad.alpmHandle
}

func (ad *ArchDiff) Release() {
	if ad.alpmHandle != nil {
		ad.alpmHandle.Release()
	}
}

func (ad *ArchDiff) LocalDb() *alpm.Db {
	if ad.localDb == nil {
		var err error
		ad.localDb, err = ad.Alpm().LocalDb()
		if err != nil {
			log.Fatalf("Error loading local DB: %s", err)
		}
	}
	return ad.localDb
}

func (ad *ArchDiff) BackupFile() []File {
	if ad.backupFile == nil {
		ad.LocalDb().PkgCache().ForEach(func(pkg alpm.Package) error {
			return pkg.Backup().ForEach(func(bf alpm.BackupFile) error {
				ad.backupFile = append(ad.backupFile, File{Name: bf.Name, Hash: bf.Hash})
				return nil
			})
		})
	}
	return ad.backupFile
}

func (ad *ArchDiff) AllFile() []File {
	if ad.allFile == nil {
		filepath.Walk(
			ad.Root,
			func(path string, info os.FileInfo, err error) error {
				if ad.IsIgnored(path) {
					if info.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				if info.IsDir() {
					return nil
				}
				if err != nil {
					if os.IsPermission(err) {
						if !ad.Silent {
							log.Printf("Skipping file: %s", err)
						}
						return nil
					}
					log.Fatalf("Error finding unpackaged file: %s", err)
				}
				ad.allFile = append(ad.allFile, File{Name: path[1:]})
				return nil
			})
	}
	return ad.allFile
}

func (ad *ArchDiff) AllPackageFile() []File {
	if ad.allPackageFile == nil {
		ad.LocalDb().PkgCache().ForEach(func(pkg alpm.Package) error {
			for _, file := range pkg.Files() {
				ad.allPackageFile = append(ad.allPackageFile, File{Name: file.Name})
			}
			return nil
		})
	}
	return ad.allPackageFile
}

func (ad *ArchDiff) ModifiedBackupFile() []File {
	if ad.modifiedBackupFile == nil {
		for _, file := range ad.BackupFile() {
			fullname := filepath.Join(ad.Root, file.Name)
			if ad.IsIgnored(fullname) {
				continue
			}
			actual, err := filehash(fullname)
			if err != nil {
				if !ad.Silent {
					log.Printf("Error calculating current hash: %s", err)
				}
				continue
			}
			if actual != file.Hash {
				ad.modifiedBackupFile = append(ad.modifiedBackupFile, file)
			}
		}
	}
	return ad.modifiedBackupFile
}

func (ad *ArchDiff) UnpackagedFile() []File {
	if ad.unpackagedFile == nil {
		for _, file := range ad.AllFile() {
			if !contains(file.Name, ad.AllPackageFile()) {
				ad.unpackagedFile = append(ad.unpackagedFile, file)
			}
		}
	}
	return ad.unpackagedFile
}

func (ad *ArchDiff) RepoFile() []File {
	if ad.repoFile == nil {
		filepath.Walk(ad.Repo, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				if !ad.Silent {
					log.Printf("RepoFile Walk error: %s", err)
				}
				return nil
			}
			if info.IsDir() {
				return nil
			}
			ad.repoFile = append(ad.repoFile, File{Name: path})
			return nil
		})
	}
	return ad.repoFile
}

func (ad *ArchDiff) DiffRepoFile() []File {
	if ad.diffRepoFile == nil {
		for _, file := range ad.RepoFile() {
			realpath := filepath.Join(ad.Root, file.Name)
			repopath := filepath.Join(ad.Repo, file.Name)
			realhash, err := filehash(realpath)
			if err != nil && !os.IsNotExist(err) {
				if os.IsPermission(err) {
					if !ad.Silent {
						log.Printf("Skipping file: %s", err)
					}
					continue
				}
				log.Fatalf("Error looking for modified repo files (real): %s", err)
			}
			repohash, err := filehash(repopath)
			if err != nil && !os.IsNotExist(err) {
				if os.IsPermission(err) {
					if !ad.Silent {
						log.Printf("Skipping file: %s", err)
					}
					continue
				}
				log.Fatalf("Error looking for modified repo files (repo): %s", err)
			}
			if realhash != repohash {
				ad.diffRepoFile = append(ad.diffRepoFile, file)
			}
		}
	}
	return ad.diffRepoFile
}

func (ad *ArchDiff) MissingInRepo() []File {
	if ad.missingInRepo == nil {
		for _, file := range ad.ModifiedBackupFile() {
			if !contains(file.Name, ad.RepoFile()) {
				ad.missingInRepo = append(ad.missingInRepo, file)
			}
		}
		for _, file := range ad.UnpackagedFile() {
			if !contains(file.Name, ad.RepoFile()) {
				ad.missingInRepo = append(ad.missingInRepo, file)
			}
		}
	}
	return ad.missingInRepo
}

func (ad *ArchDiff) ListNamed(name string) []File {
	switch name {
	case "missing-in-repo":
		return ad.MissingInRepo()
	case "different-in-repo":
		return ad.DiffRepoFile()
	case "package-backups":
		return ad.BackupFile()
	case "all":
		return ad.AllFile()
	case "package":
		return ad.AllPackageFile()
	case "modified-backups":
		return ad.ModifiedBackupFile()
	case "unpackaged":
		return ad.UnpackagedFile()
	case "repo":
		return ad.RepoFile()
	}
	log.Fatalf("unknown list name: %s", name)
	panic("not reached")
}

func (ad *ArchDiff) CommandLs(args []string) {
	for _, name := range args[1:] {
		fmt.Println(name)
		for _, file := range ad.ListNamed(name) {
			fmt.Println(" ", file.Name)
		}
	}
}

func (ad *ArchDiff) CommandStatus(args []string) {
	ad.CommandLs([]string{"ls", "missing-in-repo", "different-in-repo"})
}

func olderNewer(aPath, bPath string) (older, newer string, err error) {
	aStat, err := os.Stat(aPath)
	if err != nil {
		return "", "", err
	}
	bStat, err := os.Stat(bPath)
	if err != nil {
		return "", "", err
	}
	if aStat.ModTime().After(bStat.ModTime()) {
		return bPath, aPath, nil
	}
	return aPath, bPath, nil
}

func (ad *ArchDiff) copyFile(dst, src string) {
	if ad.DryRun {
		fmt.Println("cp", src, dst)
		return
	}
	_, err := cp.Single(dst, src)
	if err != nil && !ad.Silent {
		log.Printf("failed to copy file %s to %s: %s", src, dst, err)
	}
}

func (ad *ArchDiff) CommandSync(args []string) {
	for _, file := range ad.MissingInRepo() {
		ad.copyFile(
			filepath.Join(ad.Repo, file.Name),
			filepath.Join(ad.Root, file.Name))
	}
	for _, file := range ad.DiffRepoFile() {
		older, newer, err := olderNewer(
			filepath.Join(ad.Root, file.Name),
			filepath.Join(ad.Repo, file.Name))
		if err != nil {
			log.Fatalf("Failed to identify newer file: %s", err)
		}
		ad.copyFile(older, newer)
	}
}

func (ad *ArchDiff) CommandUnknown(args []string) {
	log.Fatalf("unknown command: %s", strings.Join(args, " "))
}

func (ad *ArchDiff) Usage() {
	log.Fatalf("usage: archdiff [ls | status | sync]")
}

func (ad *ArchDiff) Command(args []string) {
	if len(args) == 0 {
		ad.Usage()
	}
	switch args[0] {
	case "ls":
		ad.CommandLs(args)
	case "status":
		ad.CommandStatus(args)
	case "sync":
		ad.CommandSync(args)
	default:
		ad.CommandUnknown(args)
	}
}

func main() {
	ad := &ArchDiff{}
	flag.IntVar(&ad.MaxProcs, "max-procs", runtime.NumCPU()*2, "go max procs")
	flag.BoolVar(&ad.Verbose, "verbose", false, "verbose")
	flag.BoolVar(&ad.Silent, "silent", false, "suppress errors")
	flag.BoolVar(&ad.DryRun, "f", true, "dry run")
	flag.StringVar(&ad.Root, "root", "/", "set an alternate installation root")
	flag.StringVar(
		&ad.DB, "dbpath", "/var/lib/pacman", "set an alternate database location")
	flag.StringVar(&ad.Repo, "repo", "", "repo directory")
	flag.StringVar(&ad.IgnoreFile, "ignore", "", "ignore file")
	flag.Parse()
	flagconfig.Parse()

	runtime.GOMAXPROCS(ad.MaxProcs)
	ad.Command(flag.Args())
}
