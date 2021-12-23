# Jomics

Jomics is a simple comic reader server. It supports cbz and cbr and can handle folders.
It has some basic `ComicInfo.xml` support. (Probably it can be developed further.)

![jomics](jomics.png "Jomics screenshot")

## Installation

You need a go compiler, probably a pretty new version. Clone the repo, and run

`go get && go build`

All data will be embedded into the `jomics` binary.

## Usage
```
  Usage of ./jomics:
    -addr string
          Server address. (default ":8080")
    -root string
          Comic collection root. (default ".")
    -th int
          Front page thumb nail size. (default 400)
```
And point your favorite browser to `localhost:8080` if you are using the address.

## Security
There is none. It is just a comics reader server.

## Third party packages
 * The folder icon is taken from http://www.clker.com/clipart-simple-file-folder.html (resized & included)
 * The CSS framework used https://picocss.com/ (included)
 * http://github.com/OpenPeeDeeP/xdg for cache directory.
 * http://github.com/disintegration/imaging is used to resize images.
 * http://github.com/gen2brain/go-unarr for uncompressing zip and rar archives.

## TODO
Currently jomics is feature complete. I have no plans to add any features, but it might change once I've used jomics for some time.

## License
MIT

