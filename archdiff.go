package main

import (
	"bytes"
	"crypto/md5"
	"flag"
	"fmt"
	"github.com/nshah/go.flagconfig"
	"github.com/remyoudompheng/go-alpm"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type File struct {
	Name string
	Hash string
}

type ArchDiff struct {
	Verbose     bool
	Root        string
	DB          string
	Repo        string
	IgnoreGlobs []string

	backupFile         []File
	modifiedBackupFile []File
	localDb            *alpm.Db
	alpmHandle         *alpm.Handle
	allPackageFile     []File
	allEtcFile         []File
	unpackagedFile     []File
	repoFile           []File
	modifiedRepoFile   []File
	missingInRepo      []File
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

func (ad *ArchDiff) IsIgnored(path string) bool {
	for _, glob := range ad.IgnoreGlobs {
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

func (ad *ArchDiff) AllEtcFile() []File {
	if ad.allEtcFile == nil {
		filepath.Walk(
			filepath.Join(ad.Root, "etc"),
			func(path string, info os.FileInfo, err error) error {
				if ad.IsIgnored(path) {
					return nil
				}
				if info.IsDir() {
					return nil
				}
				if err != nil {
					if os.IsPermission(err) {
						log.Printf("Skipping file: %s", err)
						return nil
					}
					log.Fatalf("Error finding unpackaged file: %s", err)
				}
				ad.allEtcFile = append(ad.allEtcFile, File{Name: path[1:]})
				return nil
			})
	}
	return ad.allEtcFile
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
				if os.IsPermission(err) {
					log.Printf("Skipping file: %s\n", err)
					continue
				}
				log.Fatalf("Error calculating actual hash: %s", err)
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
		for _, file := range ad.AllEtcFile() {
			if !contains(file.Name, ad.AllPackageFile()) {
				ad.unpackagedFile = append(ad.unpackagedFile, file)
			}
		}
	}
	return ad.unpackagedFile
}

func (ad *ArchDiff) RepoFile() []File {
	if ad.repoFile == nil {
		cmd := exec.Command("git", "ls-files")
		cmd.Dir = ad.Repo
		out, err := cmd.Output()
		if err != nil {
			log.Fatalf("Error listing repo files: %s", err)
		}
		buf := bytes.NewBuffer(out)
		for {
			line, err := buf.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					break
				}
				log.Fatalf("Error parsing repo listing: %s", err)
			}
			ad.repoFile = append(
				ad.repoFile, File{Name: line[:len(line)-1]}) // drop trailing \n
		}
	}
	return ad.repoFile
}

func (ad *ArchDiff) ModifiedRepoFile() []File {
	if ad.modifiedRepoFile == nil {
		for _, file := range ad.RepoFile() {
			realpath := filepath.Join(ad.Root, file.Name)
			repopath := filepath.Join(ad.Repo, file.Name)
			realhash, err := filehash(realpath)
			if err != nil && !os.IsNotExist(err) {
				if os.IsPermission(err) {
					log.Printf("Skipping file: %s", err)
					continue
				}
				log.Fatalf("Error looking for modified repo files (real): %s", err)
			}
			repohash, err := filehash(repopath)
			if err != nil && !os.IsNotExist(err) {
				if os.IsPermission(err) {
					log.Printf("Skipping file: %s", err)
					continue
				}
				log.Fatalf("Error looking for modified repo files (repo): %s", err)
			}
			if realhash != repohash {
				ad.modifiedRepoFile = append(ad.modifiedRepoFile, file)
			}
		}
	}
	return ad.modifiedRepoFile
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
	case "modified-in-repo":
		return ad.ModifiedRepoFile()
	case "package-backups":
		return ad.BackupFile()
	case "etc":
		return ad.AllEtcFile()
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

func (ad *ArchDiff) CommandUnknown(args []string) {
	log.Fatalf("unknown command: %s", strings.Join(args, " "))
}

func (ad *ArchDiff) Command(args []string) {
	switch args[0] {
	case "ls":
		ad.CommandLs(args)
	default:
		ad.CommandUnknown(args)
	}
}

func main() {
	ad := &ArchDiff{}
	flag.BoolVar(&ad.Verbose, "verbose", false, "verbose")
	flag.StringVar(&ad.Root, "root", "/", "set an alternate installation root")
	flag.StringVar(
		&ad.DB, "dbpath", "/var/lib/pacman", "set an alternate database location")
	flag.StringVar(&ad.Repo, "repo", "", "repo directory")
	ad.IgnoreGlobs = []string{
		"/etc/group",
		"/etc/gshadow",
		"/etc/passwd",
		"/etc/shadow",
		"/etc/shells",
		"/etc/.pwd.lock",
		"/etc/group-",
		"/etc/gshadow-",
		"/etc/ld.so.cache",
		"/etc/pacman.d/gnupg/*",
		"/etc/passwd-",
		"/etc/profile.d/locale.sh",
		"/etc/rndc.key",
		"/etc/shadow-",
		"/etc/ssh/ssh_host_*key*",
		"/etc/ssl/certs/*", /**/
	}

	flag.Parse()
	flagconfig.Parse()

	ad.Command(flag.Args())
}
