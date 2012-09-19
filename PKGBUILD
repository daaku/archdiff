pkgname=archdiff
pkgver=1
pkgrel=1
pkgdesc="A tool to view a 'system' diff for Arch Linux systems."
arch=(x86_64 i686)
url="https://github.com/daaku/archdiff"
source=(archdiff.go)
license=('apache2')

package() {
  install -d $pkgdir/usr/bin
  go build -o $pkgdir/usr/bin/archdiff
}
