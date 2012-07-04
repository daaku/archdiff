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

func modified(h *alpm.Handle) ([]string, error) {
	db, err := h.LocalDb()
	if err != nil {
		return nil, err
	}
	db.PkgCache().ForEach(func(pkg alpm.Package) error {
		for _, file := range pkg.Backup().Slice() {
			actual, err := filehash(filepath.Join(*root, file.Name))
			if err != nil {
				fmt.Printf("Error calculating actual hash: %s\n", err)
			}
			if actual != file.Hash {
				fmt.Printf("Different hash: %s %s %s\n", pkg.Name(), file, actual)
			}
		}
		return nil
	})
	return nil, nil
}

func main() {
	handle, err := alpm.Init(*root, *dbpath)
	if err != nil {
		log.Fatalf("Failed to initialize pacman: %s", err)
	}
	defer handle.Release()

	log.Println(modified(handle))
}
