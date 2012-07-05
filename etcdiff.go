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
)

type EtcDiff struct {
	Verbose     bool
	Root        string
	DB          string
	Repo        string
	IgnoreGlobs []string

	backupFile         []alpm.BackupFile
	modifiedBackupFile []alpm.BackupFile
	localDb            *alpm.Db
	alpmHandle         *alpm.Handle
	allPackageFile     []alpm.File
	unpackagedFile     []string
	repoFile           []string
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

func inList(path string, list []alpm.File) bool {
	for _, file := range list {
		if file.Name == path {
			return true
		}
	}
	return false
}

func (e *EtcDiff) IsIgnored(path string) bool {
	for _, glob := range e.IgnoreGlobs {
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

func (e *EtcDiff) Alpm() *alpm.Handle {
	if e.alpmHandle == nil {
		var err error
		e.alpmHandle, err = alpm.Init(e.Root, e.DB)
		if err != nil {
			log.Fatalf("Failed to initialize pacman: %s", err)
		}
	}
	return e.alpmHandle
}

func (e *EtcDiff) Release() {
	if e.alpmHandle != nil {
		e.alpmHandle.Release()
	}
}

func (e *EtcDiff) LocalDb() *alpm.Db {
	if e.localDb == nil {
		var err error
		e.localDb, err = e.Alpm().LocalDb()
		if err != nil {
			log.Fatalf("Error loading local DB: %s", err)
		}
	}
	return e.localDb
}

func (e *EtcDiff) BackupFile() []alpm.BackupFile {
	if e.backupFile == nil {
		e.LocalDb().PkgCache().ForEach(func(pkg alpm.Package) error {
			return pkg.Backup().ForEach(func(bf alpm.BackupFile) error {
				e.backupFile = append(e.backupFile, bf)
				return nil
			})
		})
	}
	return e.backupFile
}

func (e *EtcDiff) AllPackageFile() []alpm.File {
	if e.allPackageFile == nil {
		e.LocalDb().PkgCache().ForEach(func(pkg alpm.Package) error {
			e.allPackageFile = append(e.allPackageFile, pkg.Files()...)
			return nil
		})
	}
	return e.allPackageFile
}

func (e *EtcDiff) ModifiedBackupFile() []alpm.BackupFile {
	if e.modifiedBackupFile == nil {
		for _, file := range e.BackupFile() {
			fullname := filepath.Join(e.Root, file.Name)
			if e.IsIgnored(fullname) {
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
				e.modifiedBackupFile = append(e.modifiedBackupFile, file)
			}
		}
	}
	return e.modifiedBackupFile
}

func (e *EtcDiff) UnpackagedFile() []string {
	if e.unpackagedFile == nil {
		filepath.Walk(
			filepath.Join(e.Root, "etc"),
			func(path string, info os.FileInfo, err error) error {
				if e.IsIgnored(path) {
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
				if !inList(path[1:], e.AllPackageFile()) {
					e.unpackagedFile = append(e.unpackagedFile, path[1:])
				}
				return nil
			})
	}
	return e.unpackagedFile
}

func (e *EtcDiff) RepoFile() []string {
	if e.repoFile == nil {
		cmd := exec.Command("git", "ls-files")
		cmd.Dir = e.Repo
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
			e.repoFile = append(e.repoFile, line[:len(line)-1]) // drop trailing \n
		}
	}
	return e.repoFile
}

func main() {
	e := &EtcDiff{}
	flag.BoolVar(&e.Verbose, "verbose", false, "verbose")
	flag.StringVar(&e.Root, "root", "/", "set an alternate installation root")
	flag.StringVar(
		&e.DB, "dbpath", "/var/lib/pacman", "set an alternate database location")
	flag.StringVar(&e.Repo, "repo", "", "repo directory")
	e.IgnoreGlobs = []string{
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

	log.Printf("%+v", e.ModifiedBackupFile())
	log.Printf("%+v", e.UnpackagedFile())
	log.Printf("%+v", e.RepoFile())
}
