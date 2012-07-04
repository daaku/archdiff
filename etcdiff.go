package main

import (
	"crypto/md5"
	"flag"
	"fmt"
	"github.com/remyoudompheng/go-alpm"
	"io"
	"log"
	"os"
	"path/filepath"
)

var (
	verbose = flag.Bool("v", false, "verbose")
	root    = flag.String("root", "/", "set an alternate installation root")
	dbpath  = flag.String(
		"dbpath", "/var/lib/pacman", "set an alternate database location")
)

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
		actual, err := filehash(filepath.Join(*root, file.Name))
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

func main() {
	handle, err := alpm.Init(*root, *dbpath)
	if err != nil {
		log.Fatalf("Failed to initialize pacman: %s", err)
	}
	defer handle.Release()

	backupFiles, err := backups(handle)
	if err != nil {
		log.Fatalf("Failed to retrieve backups list: %s", err)
	}
	log.Printf("%+v", backupFiles)

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
}
