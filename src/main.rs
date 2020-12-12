use anyhow::{anyhow, Context, Result};
use ignore::gitignore::{Gitignore, GitignoreBuilder};
use log::error;
use rayon::prelude::*;
use std::collections::{HashMap, HashSet};
use std::fmt::Display;
use std::os::unix::ffi::OsStrExt;
use structopt::StructOpt;
use walkdir::WalkDir;

#[derive(StructOpt)]
#[structopt(name = "colaz")]
struct Args {
    #[structopt(long, help = "root dir", default_value = "/")]
    root: String,
    #[structopt(long, help = "database dir", default_value = "/var/lib/pacman")]
    dbpath: String,
    #[structopt(long, help = "repo dir", default_value = "/usr/share/archdiff")]
    repo: String,
    #[structopt(long, help = "ignore dir", default_value = "/etc/archdiff/ignore")]
    ignore: String,
}

struct App {
    alpm: alpm::Alpm,
    ignore: Gitignore,
    args: Args,
}

fn hash_file<P: AsRef<std::path::Path>>(path: P) -> Result<String> {
    let hash = alpm::compute_md5sum(path.as_ref().as_os_str().as_bytes())
        .map_err(|_| anyhow!("failed to hash {}", path.as_ref().display()))?;
    Ok(hash)
}

fn hash_file_logged<P: AsRef<std::path::Path>>(path: P) -> Option<String> {
    match hash_file(&path) {
        Ok(hash) => Some(hash),
        Err(err) => {
            error!("IO error for operation on {:?}: {}", path.as_ref(), err);
            None
        }
    }
}

fn filter_map_error<Error: Display, O>(result: std::result::Result<O, Error>) -> Option<O> {
    match result {
        Ok(o) => Some(o),
        Err(err) => {
            error!("{}", err);
            None
        }
    }
}

// TODO: command to sync /usr/share/archdiff automatically

impl App {
    #[allow(clippy::new_ret_no_self)]
    fn new(mut args: Args) -> Result<Self> {
        if !args.root.ends_with('/') {
            args.root.push('/');
        }
        if !args.repo.ends_with('/') {
            args.repo.push('/');
        }
        Ok(Self {
            alpm: alpm::Alpm::new(args.root.as_bytes(), args.dbpath.as_bytes())?,
            ignore: Self::build_gitignore(&args.ignore)?,
            args,
        })
    }

    fn build_gitignore(ignore: &str) -> Result<Gitignore> {
        let mut gi_builder = GitignoreBuilder::new("/");
        let ignores = std::fs::read_dir(ignore)
            .with_context(|| format!("failed to read directory {}", ignore))?;
        for path in ignores {
            let path = path?;
            let oerr = gi_builder.add(path.path());
            if let Some(err) = oerr {
                return Err(err.into());
            }
        }
        Ok(gi_builder.build()?)
    }

    fn run(&self) {
        let mut pkg_files = HashSet::new();
        let mut pkg_backup_files = HashMap::new();
        for pkg in self.alpm.localdb().pkgs() {
            pkg_files.extend(pkg.files().files().iter().map(|f| f.name().to_string()));
            pkg_backup_files.extend(
                pkg.backup()
                    .iter()
                    .map(|b| (b.name().to_string(), b.hash().to_string())),
            );
        }

        let root = &self.args.root;
        let ignored = &self.ignore;
        let root_len = self.args.root.len();
        let repo_len = self.args.repo.len();

        let mut all = vec![];

        // untracked files on disk
        WalkDir::new(&self.args.root)
            .into_iter()
            .filter_entry(|de| {
                self.ignore
                    .matched(de.path(), de.file_type().is_dir())
                    .is_none()
            })
            .filter_map(filter_map_error)
            .for_each(|de| {
                if de.file_type().is_dir() {
                    return;
                }
                let path = &de.path().to_string_lossy()[root_len..];
                let removed = pkg_files.remove(path);
                if !removed {
                    all.push(('?', path.to_string()));
                }
            });

        // repo files that have been changed
        WalkDir::new(&self.args.repo)
            .into_iter()
            .filter_map(filter_map_error)
            .for_each(|de| {
                if de.file_type().is_dir() {
                    return;
                }
                let path = &de.path().to_string_lossy()[repo_len..];
                pkg_backup_files.remove(path);
                let repo_hash = match hash_file_logged(de.path()) {
                    None => return,
                    Some(h) => h,
                };
                let actual_hash = match hash_file_logged(&format!("{}{}", &root, path)) {
                    None => return,
                    Some(h) => h,
                };
                if repo_hash != actual_hash {
                    all.push(('R', path.to_string()));
                }
            });

        // deleted files from packages
        all.par_extend(pkg_files.into_par_iter().filter_map(|p| {
            let fp = format!("{}{}", &root, &p);
            if ignored.matched(&fp, false).is_ignore() {
                None
            } else {
                match std::fs::metadata(&fp).with_context(|| format!("failed to stat {}", fp)) {
                    Err(_) => Some(('D', p)),
                    Ok(_) => None,
                }
            }
        }));

        // backup files that have been changed
        all.par_extend(
            pkg_backup_files
                .into_par_iter()
                .filter_map(|(p, expected_hash)| {
                    let fp = format!("{}{}", &root, &p);
                    if ignored.matched_path_or_any_parents(&fp, false).is_ignore() {
                        None
                    } else {
                        hash_file_logged(&fp).and_then(|actual_hash| {
                            if expected_hash == actual_hash {
                                None
                            } else {
                                Some(('B', p))
                            }
                        })
                    }
                }),
        );

        all.sort_by(|(_, a), (_, b)| a.cmp(b));
        all.iter()
            .for_each(|(c, n)| println!("{} {}{}", c, &root, n));
    }
}

fn main() -> Result<()> {
    pretty_env_logger::init();
    App::new(Args::from_args())?.run();
    Ok(())
}
