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

type FileList map[string]File

type Glob interface {
	Match(name string) bool
}

type realGlob string
type prefixGlob string

type ArchDiff struct {
	Verbose    bool
	Silent     bool
	Root       string
	DB         string
	Repo       string
	IgnoreFile string
	MaxProcs   int

	ignoreGlob         []Glob
	backupFile         FileList
	modifiedBackupFile FileList
	localDb            *alpm.Db
	alpmHandle         *alpm.Handle
	allPackageFile     FileList
	allFile            FileList
	unpackagedFile     FileList
	repoFile           FileList
	diffRepoFile       FileList
	missingInRepo      FileList
}

func (l FileList) Add(f File) {
	l[f.Name] = f
}

func (l FileList) Contains(name string) bool {
	_, ok := l[name]
	return ok
}

func (g realGlob) Match(path string) bool {
	matched, err := filepath.Match(string(g), path)
	if err != nil {
		log.Fatalf("Match error: %s", err)
	}
	return matched
}

func (g prefixGlob) Match(path string) bool {
	return strings.HasPrefix(path, string(g))
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

func (ad *ArchDiff) IgnoreGlob() []Glob {
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
			if strings.IndexAny(l, "*?[") > -1 {
				ad.ignoreGlob = append(ad.ignoreGlob, realGlob(l))
			} else {
				ad.ignoreGlob = append(ad.ignoreGlob, prefixGlob(l))
			}
		}
	}
	return ad.ignoreGlob
}

func (ad *ArchDiff) IsIgnored(path string) bool {
	for _, glob := range ad.IgnoreGlob() {
		if glob.Match(path) {
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

func (ad *ArchDiff) BackupFile() FileList {
	if ad.backupFile == nil {
		ad.backupFile = make(FileList)
		ad.LocalDb().PkgCache().ForEach(func(pkg alpm.Package) error {
			return pkg.Backup().ForEach(func(bf alpm.BackupFile) error {
				ad.backupFile[bf.Name] = File{Name: bf.Name, Hash: bf.Hash}
				return nil
			})
		})
	}
	return ad.backupFile
}

func (ad *ArchDiff) AllFile() FileList {
	if ad.allFile == nil {
		ad.allFile = make(FileList)
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
				name := path[1:]
				ad.allFile.Add(File{Name: name})
				return nil
			})
	}
	return ad.allFile
}

func (ad *ArchDiff) AllPackageFile() FileList {
	if ad.allPackageFile == nil {
		ad.allPackageFile = make(FileList)
		ad.LocalDb().PkgCache().ForEach(func(pkg alpm.Package) error {
			for _, file := range pkg.Files() {
				ad.allPackageFile.Add(File{Name: file.Name})
			}
			return nil
		})
	}
	return ad.allPackageFile
}

func (ad *ArchDiff) ModifiedBackupFile() FileList {
	if ad.modifiedBackupFile == nil {
		ad.modifiedBackupFile = make(FileList)
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
				ad.modifiedBackupFile.Add(file)
			}
		}
	}
	return ad.modifiedBackupFile
}

func (ad *ArchDiff) UnpackagedFile() FileList {
	if ad.unpackagedFile == nil {
		ad.unpackagedFile = make(FileList)
		allPackageFile := ad.AllPackageFile()
		for _, file := range ad.AllFile() {
			if !allPackageFile.Contains(file.Name) {
				ad.unpackagedFile.Add(file)
			}
		}
	}
	return ad.unpackagedFile
}

func (ad *ArchDiff) RepoFile() FileList {
	if ad.repoFile == nil {
		ad.repoFile = make(FileList)
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
			name := strings.Replace(path, ad.Repo, "", 1)
			if name[0] == '/' {
				name = name[1:]
			}
			ad.repoFile.Add(File{Name: name})
			return nil
		})
	}
	return ad.repoFile
}

func (ad *ArchDiff) DiffRepoFile() FileList {
	if ad.diffRepoFile == nil {
		ad.diffRepoFile = make(FileList)
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
				ad.diffRepoFile.Add(file)
			}
		}
	}
	return ad.diffRepoFile
}

func (ad *ArchDiff) MissingInRepo() FileList {
	if ad.missingInRepo == nil {
		ad.missingInRepo = make(FileList)
		repoFile := ad.RepoFile()
		for _, file := range ad.ModifiedBackupFile() {
			if !repoFile.Contains(file.Name) {
				ad.missingInRepo.Add(file)
			}
		}
		for _, file := range ad.UnpackagedFile() {
			if !repoFile.Contains(file.Name) {
				ad.missingInRepo.Add(file)
			}
		}
	}
	return ad.missingInRepo
}

func (ad *ArchDiff) ListNamed(name string) FileList {
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

func (ad *ArchDiff) CommandUnknown(args []string) {
	log.Fatalf("unknown command: %s", strings.Join(args, " "))
}

func (ad *ArchDiff) Usage() {
	log.Fatalf("usage: archdiff [ls | status]")
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
	default:
		ad.CommandUnknown(args)
	}
}

func main() {
	ad := &ArchDiff{}
	flag.IntVar(&ad.MaxProcs, "max-procs", runtime.NumCPU()*2, "go max procs")
	flag.BoolVar(&ad.Verbose, "verbose", false, "verbose")
	flag.BoolVar(&ad.Silent, "silent", false, "suppress errors")
	flag.StringVar(&ad.Root, "root", "/", "set an alternate installation root")
	flag.StringVar(
		&ad.DB, "dbpath", "/var/lib/pacman", "set an alternate database location")
	flag.StringVar(&ad.Repo, "repo", "/usr/share/archdiff", "repo directory")
	flag.StringVar(&ad.IgnoreFile, "ignore", "", "ignore file")
	flag.Parse()
	flagconfig.Parse()

	runtime.GOMAXPROCS(ad.MaxProcs)
	ad.Command(flag.Args())
}
