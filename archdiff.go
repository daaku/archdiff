// Command archdiff implements a tool to view and manipulate a "system
// level diff" of sorts. It's somewhat akin to the "things that differ"
// if a new system was given the exact current set of packages
// combined with a target directory that can be considered an "overlay"
// on top of the packages for things like configuration and or ignored
// data.
package main

import (
	"bufio"
	"crypto/md5"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sort"
	"strings"

	"github.com/daaku/go.alpm"
	"github.com/gobwas/glob"
	"github.com/pkg/errors"
)

type Glob interface {
	Match(name string) bool
}

type simpleGlob string

func (g simpleGlob) Match(path string) bool {
	if path == string(g) {
		return true
	}
	return strings.HasPrefix(path, string(g)+"/")
}

func filehash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", errors.WithStack(err)
	}
	defer file.Close()
	h := md5.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", errors.WithStack(err)
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func contains(a []string, x string) bool {
	i := sort.SearchStrings(a, x)
	if i == len(a) {
		return false
	}
	return a[i] == x
}

type ArchDiff struct {
	Root       string
	DB         string
	Repo       string
	IgnoreDir  string
	CpuProfile string

	localDB *alpm.Db
	alpm    *alpm.Handle

	ignoreGlob         []Glob
	backupFile         map[string]string
	allFile            []string
	packageFile        []string
	repoFile           []string
	modifiedBackupFile []string
	unpackagedFile     []string
	diffRepoFile       []string
}

func (ad *ArchDiff) buildIgnoreGlob() error {
	return errors.WithStack(filepath.Walk(
		ad.IgnoreDir,
		func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return errors.WithStack(err)
			}
			if info.IsDir() {
				return nil
			}
			f, err := os.Open(path)
			if err != nil {
				return errors.WithStack(err)
			}
			defer f.Close()

			sc := bufio.NewScanner(f)
			for sc.Scan() {
				l := sc.Text()
				if len(l) == 0 {
					continue
				}
				if l[0] == '#' {
					continue
				}
				if strings.IndexAny(l, "*?[") > -1 {
					g, err := glob.Compile(l)
					if err != nil {
						return errors.WithStack(err)
					}
					ad.ignoreGlob = append(ad.ignoreGlob, g)
				} else {
					ad.ignoreGlob = append(ad.ignoreGlob, simpleGlob(l))
				}
			}
			return errors.WithStack(sc.Err())
		},
	))
}

func (ad *ArchDiff) isIgnored(path string) bool {
	for _, glob := range ad.ignoreGlob {
		if glob.Match(path) {
			return true
		}
	}
	return false
}

func (ad *ArchDiff) initAlpm() error {
	var err error
	ad.alpm, err = alpm.Init(ad.Root, ad.DB)
	if err != nil {
		return errors.WithStack(err)
	}
	ad.localDB, err = ad.alpm.LocalDb()
	return errors.WithStack(err)
}

func (ad *ArchDiff) buildAllFile() error {
	return errors.WithStack(filepath.Walk(
		ad.Root,
		func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return errors.WithStack(err)
			}
			if ad.isIgnored(path) {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if info.IsDir() {
				return nil
			}
			ad.allFile = append(ad.allFile, path)
			return nil
		}))
}

func (ad *ArchDiff) buildPackageFile() error {
	err := ad.localDB.PkgCache().ForEach(func(pkg alpm.Package) error {
		for _, file := range pkg.Files() {
			ad.packageFile = append(ad.packageFile, filepath.Join("/", file.Name))
		}
		return nil
	})
	sort.Strings(ad.packageFile)
	return errors.WithStack(err)
}

func (ad *ArchDiff) buildBackupFile() error {
	ad.backupFile = make(map[string]string)
	return errors.WithStack(
		ad.localDB.PkgCache().ForEach(func(pkg alpm.Package) error {
			return pkg.Backup().ForEach(func(bf alpm.BackupFile) error {
				ad.backupFile[filepath.Join("/", bf.Name)] = bf.Hash
				return nil
			})
		}))
}

func (ad *ArchDiff) buildRepoFile() error {
	err := filepath.Walk(ad.Repo,
		func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() {
				return nil
			}
			name := strings.Replace(path, ad.Repo, "", 1)
			ad.repoFile = append(ad.repoFile, name)
			return nil
		})
	sort.Strings(ad.repoFile)
	return errors.WithStack(err)
}

func (ad *ArchDiff) buildUnpackagedFile() error {
	for _, file := range ad.allFile {
		if !contains(ad.packageFile, file) {
			ad.unpackagedFile = append(ad.unpackagedFile, file)
		}
	}
	return nil
}

func (ad *ArchDiff) buildModifiedBackupFile() error {
	for file, hash := range ad.backupFile {
		if contains(ad.repoFile, file) {
			continue
		}
		fullname := filepath.Join(ad.Root, file)
		if ad.isIgnored(fullname) {
			continue
		}
		if _, err := os.Stat(fullname); os.IsNotExist(err) {
			continue
		}
		actual, err := filehash(fullname)
		if err != nil {
			return errors.WithStack(err)
		}
		if actual != hash {
			ad.modifiedBackupFile = append(ad.modifiedBackupFile, file)
		}
	}
	return nil
}

func (ad *ArchDiff) buildDiffRepoFile() error {
	for _, file := range ad.repoFile {
		realpath := filepath.Join(ad.Root, file)
		repopath := filepath.Join(ad.Repo, file)
		realhash, err := filehash(realpath)
		if err != nil && !os.IsNotExist(err) {
			return errors.WithStack(err)
		}
		repohash, err := filehash(repopath)
		if err != nil && !os.IsNotExist(err) {
			return errors.WithStack(err)
		}
		if realhash != repohash {
			ad.diffRepoFile = append(ad.diffRepoFile, file)
		}
	}
	return nil
}

func Main() error {
	var ad ArchDiff
	flag.StringVar(&ad.Root, "root", "/", "set an alternate installation root")
	flag.StringVar(
		&ad.DB, "dbpath", "/var/lib/pacman", "set an alternate database location")
	flag.StringVar(&ad.Repo, "repo", "/usr/share/archdiff", "repo directory")
	flag.StringVar(&ad.IgnoreDir, "ignore", "/etc/archdiff/ignore",
		"directory of ignore files")
	flag.StringVar(&ad.CpuProfile, "cpuprofile", "", "write cpu profile here")
	flag.Parse()

	if ad.CpuProfile != "" {
		f, err := os.Create(ad.CpuProfile)
		if err != nil {
			return errors.WithStack(err)
		}
		defer f.Close()
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}

	steps := []func() error{
		ad.initAlpm,
		ad.buildIgnoreGlob,
		ad.buildAllFile,
		ad.buildPackageFile,
		ad.buildBackupFile,
		ad.buildRepoFile,
		ad.buildUnpackagedFile,
		ad.buildModifiedBackupFile,
		ad.buildDiffRepoFile,
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}

	diff := make([]string, 0,
		len(ad.unpackagedFile)+len(ad.diffRepoFile)+len(ad.modifiedBackupFile))
	diff = append(diff, ad.unpackagedFile...)
	diff = append(diff, ad.diffRepoFile...)
	diff = append(diff, ad.modifiedBackupFile...)
	sort.Strings(diff)

	for _, file := range diff {
		fmt.Println(file)
	}

	return nil
}

func main() {
	if err := Main(); err != nil {
		fmt.Fprintf(os.Stderr, "%+v", err)
		os.Exit(1)
	}
}
