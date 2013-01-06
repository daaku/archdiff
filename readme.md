archdiff
========

ArchDiff provides a way to see a "diff" for your entire [Arch
Linux][arch] system. This includes showing modified config files &
unpackaged files.

It looks at a "shadow" tree to check if unpackaged files or modified config
files are already being tracked outside of pacman. This allows for cleanly
ignoring modified config files.

[arch]: http://www.archlinux.org/
