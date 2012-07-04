package main

import (
	"bytes"
	"crypto/md5"
	"flag"
	"fmt"
	"github.com/remyoudompheng/go-alpm"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

var (
	verbose = flag.Bool("v", false, "verbose")
	root    = flag.String("root", "/", "set an alternate installation root")
	dbpath  = flag.String(
		"dbpath", "/var/lib/pacman", "set an alternate database location")
	repo = flag.String("repo", safewd(), "repo directory")

	ignoreGlobs = []string{
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
)

func safewd() string {
	wd, _ := os.Getwd()
	return wd
}

func ignore(path string) bool {
	for _, glob := range ignoreGlobs {
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

func backups(h *alpm.Handle) (files []alpm.BackupFile, err error) {
	db, err := h.LocalDb()
	if err != nil {
		return nil, err
	}
	err = db.PkgCache().ForEach(func(pkg alpm.Package) error {
		return pkg.Backup().ForEach(func(bf alpm.BackupFile) error {
			files = append(files, bf)
			return nil
		})
	})
	return
}

func files(h *alpm.Handle) (files []alpm.File, err error) {
	db, err := h.LocalDb()
	if err != nil {
		return nil, err
	}
	err = db.PkgCache().ForEach(func(pkg alpm.Package) error {
		files = append(files, pkg.Files()...)
		return nil
	})
	return
}

func modified(files []alpm.BackupFile) (list []alpm.BackupFile, err error) {
	for _, file := range files {
		fullname := filepath.Join(*root, file.Name)
		if ignore(fullname) {
			continue
		}
		actual, err := filehash(fullname)
		if err != nil {
			if os.IsPermission(err) {
				log.Printf("Skipping file due to permission errors: %s\n", err)
				continue
			} else {
				return nil, fmt.Errorf("Error calculating actual hash: %s", err)
			}
		}
		if actual != file.Hash {
			list = append(list, file)
		}
	}
	return
}

func inList(path string, list []alpm.File) bool {
	for _, file := range list {
		if file.Name == path {
			return true
		}
	}
	return false
}

func unpackaged(packaged []alpm.File) (list []string, err error) {
	err = filepath.Walk(
		filepath.Join(*root, "etc"),
		func(path string, info os.FileInfo, err error) error {
			if ignore(path) {
				return nil
			}
			if info.IsDir() {
				return nil
			}
			if err != nil {
				if os.IsPermission(err) {
					log.Printf("Skipping file due to permission errors: %s", err)
					return nil
				} else {
					return err
				}
			}
			if !inList(path[1:], packaged) {
				list = append(list, path[1:])
			}
			return nil
		})
	return
}

func repoFiles() (lines []string, err error) {
	cmd := exec.Command("git", "ls-files")
	cmd.Dir = *repo
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	buf := bytes.NewBuffer(out)
	for {
		line, err := buf.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return lines, nil
			}
			return nil, err
		}
		lines = append(lines, line[:len(line)-1]) // drop trailing newline
	}
	return
}

func main() {
	flag.Parse()
	handle, err := alpm.Init(*root, *dbpath)
	if err != nil {
		log.Fatalf("Failed to initialize pacman: %s", err)
	}
	defer handle.Release()

	backupFiles, err := backups(handle)
	if err != nil {
		log.Fatalf("Failed to retrieve backups list: %s", err)
	}

	allFiles, err := files(handle)
	if err != nil {
		log.Fatalf("Failed to retrieve all files list: %s", err)
	}

	modifiedFiles, err := modified(backupFiles)
	if err != nil {
		log.Fatalf("Error finding modified files: %s", err)
	}
	log.Printf("%+v", modifiedFiles)

	unpackagedFiles, err := unpackaged(allFiles)
	if err != nil {
		log.Fatalf("Error finding unpackaged files: %s", err)
	}
	log.Printf("%+v", unpackagedFiles)

	repoFiles, err := repoFiles()
	if err != nil {
		log.Fatalf("Error finding repo files: %s", err)
	}
	log.Printf("%+v", repoFiles)
}
