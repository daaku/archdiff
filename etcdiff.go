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

func modified(files []alpm.BackupFile) (l []alpm.BackupFile, err error) {
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
			l = append(l, file)
		}
	}
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
	log.Println(backupFiles)

	modifiedFiles, err := modified(backupFiles)
	if err != nil {
		log.Fatalf("Error finding modified files: %s", err)
	}
	log.Printf("%+v", modifiedFiles)
}
