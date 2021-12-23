package main

import (
	"archive/zip"
	"bytes"
	"container/list"
	"embed"
	"fmt"
	"hash/crc32"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/jpeg"
	_ "image/png"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"text/template"

	"github.com/OpenPeeDeeP/xdg"
	"github.com/disintegration/imaging"
	"github.com/recoilme/pudge"
	_ "golang.org/x/image/webp"

	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const FRONTPAGE_HEIGHT = 400
const FRONT_IMAGE_PATH = "/fronts/"
const READ_PATH = "/read/"
const ALBUMS_PATH = "/albums/"
const ALBUM_IMAGE_PATH = "/images/"
const STATIC_PATH = "/static/"

const JOMICS_FRONT_PAGE_CACHE = "frontpage.cache"

var FOLDER_PNG_HASH = crc32.Checksum([]byte("folder.png"), crc32.IEEETable)

type comic struct {
	hash      uint32
	fname     string
	title     string
	isDir     bool
	mimeType  string
	frontPage []byte

	files *list.List
}

type jomics struct {
	home           string
	comics         map[string][]*comic
	hash2comics    map[uint32]*comic
	hash2dir       map[uint32]string
	xdg            *xdg.XDG
	frontPageCache *pudge.Db

	frontTmpl *template.Template
	pageTmpl  *template.Template
	folder    []byte
}

func (jomics *jomics) listComics(root string) {

	jomics.comics = make(map[string][]*comic)
	jomics.hash2comics = make(map[uint32]*comic)
	jomics.hash2dir = make(map[uint32]string)

	homeSet := false

	filepath.WalkDir(root, func(path string, de fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if de.IsDir() || mime.TypeByExtension(filepath.Ext(de.Name())) == "application/vnd.comicbook+zip" {

			title := strings.Title(strings.ReplaceAll(de.Name()[:len(de.Name())-len(filepath.Ext(de.Name()))], "_", " "))

			hash := crc32.Checksum([]byte(path), crc32.IEEETable)
			if de.IsDir() {
				jomics.hash2dir[hash] = path
			}

			c := &comic{
				hash:  hash,
				isDir: de.IsDir(),
				fname: path,
				title: title,
			}

			jomics.comics[filepath.Dir(path)] = append(jomics.comics[filepath.Dir(path)], c)
			jomics.hash2comics[c.hash] = c
			if !homeSet && de.IsDir() {
				jomics.home = filepath.Dir(path)
				homeSet = true
			}
		}
		return nil
	})

	for k, v := range jomics.comics {
		fmt.Println(k, len(v))
	}

}

func loadZip(fname string) (*zip.ReadCloser, []*zip.File, error) {

	r, err := zip.OpenReader(fname)
	if err != nil {
		return nil, nil, err
	}

	imgs := make([]*zip.File, 0, len(r.File))

	for j := range r.File {
		if strings.HasPrefix(mime.TypeByExtension(filepath.Ext(r.File[j].Name)), "image/") {
			imgs = append(imgs, r.File[j])
		}
	}

	sort.Slice(imgs, func(i, j int) bool {
		return imgs[i].Name < imgs[j].Name
	})

	return r, imgs, nil
}

func (jomics *jomics) loadFrontPages() {

	resizeAndSave := func(m image.Image, h uint32) []byte {
		img := imaging.Resize(m, 0, FRONTPAGE_HEIGHT, imaging.Lanczos)

		b := new(bytes.Buffer)
		if err := jpeg.Encode(b, img, nil); err != nil {
			log.Fatalf("Failed to encode jpeg: %v\n", err)
		}
		data := b.Bytes()
		jomics.frontPageCache.Set(h, data)
		return data
	}

	if present, _ := jomics.frontPageCache.Has(FOLDER_PNG_HASH); !present {
		f, err := staticFiles.Open("static/folder.png")
		if err != nil {
			log.Fatal("Can't open folder.png - missing?", err)
		}
		m, _, err := image.Decode(f)
		if err != nil {
			log.Fatal("Can't decode folder.png - corrupt? ", err)
		}

		resizeAndSave(m, FOLDER_PNG_HASH)
	}

	if err := jomics.frontPageCache.Get(FOLDER_PNG_HASH, &jomics.folder); err != nil {
		log.Fatal("Unable to load folder image from cache", err)
	}

	for i := range jomics.comics {
		for j := range jomics.comics[i] {
			if jomics.comics[i][j].isDir {
				continue
			}

			r, imgs, err := loadZip(jomics.comics[i][j].fname)

			if err != nil {
				fmt.Printf("Failed to open zipfile: %s Error: %v\n", jomics.comics[i][j].fname, err)
				continue
			}

			if present, err := jomics.frontPageCache.Has(jomics.comics[i][j].hash); err == nil && present {
				if err := jomics.frontPageCache.Get(jomics.comics[i][j].hash, &jomics.comics[i][j].frontPage); err == nil {
					continue
				} else {
					fmt.Printf("cache item error: %v -- reload image\n", err)
				}
			}

			fmt.Printf("Load & resize FrontPage: 0x%08x\n", jomics.comics[i][j].hash)
			if zf, err := imgs[0].Open(); err == nil {
				if m, _, err := image.Decode(zf); err == nil {
					jomics.comics[i][j].frontPage = resizeAndSave(m, jomics.comics[i][j].hash)
				} else {
					fmt.Printf("Failed to decode image %s in zip: %s Error: %v\n",
						imgs[0].Name, jomics.comics[i][j].fname, err)
				}
				zf.Close()
			} else {
				fmt.Printf("Failed to decompress %s from %s: Error: %v\n", imgs[0].Name, jomics.comics[i][j].fname, err)
			}
			r.Close()
		}
	}
}

/*
func (jomics *jomics) url2albumPage(root, url string) (uint32, int, error) {

	s := strings.Split(url[len(root):], "/")
	if len(s) < 2 {
		return 0, 0, fmt.Errorf("Faulty URL format")
	}
	v, err := strconv.ParseInt(s[0], 0, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("Album is not a number")
	}
	album := uint32(v)

	v, err = strconv.ParseInt(s[1], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("Page is not a number")
	}
	page := int(v)
	if _, exists := jomics.comics[album]; !exists {
		return 0, 0, fmt.Errorf("No such album")

	}

	return album, page, nil
}

func (jomics *jomics) handleAlbumImage(w http.ResponseWriter, r *http.Request) {

	album, page, err := jomics.url2albumPage(ALBUM_IMAGE_PATH, r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	zr, zf, err := loadZip(jomics.comics[album].fname)
	if err != nil {
		http.Error(w, "Error loading album file", http.StatusInternalServerError)
		return
	}
	defer zr.Close()

	if page >= len(zf) {
		http.Error(w, "Page not found", http.StatusInternalServerError)
		return
	}

	if f, err := zf[page].Open(); err == nil {
		defer f.Close()
		io.Copy(w, f)
	} else {
		http.Error(w, "Failed to open file in compressed file", http.StatusInternalServerError)
	}
}

type Page struct {
	Title        string
	First        string
	Last         string
	HasPrev      bool
	PageImageUrl string
	Prev         string
	HasNext      bool
	Next         string
}

func (jomics *jomics) handleShowAlbum(w http.ResponseWriter, r *http.Request) {

	album, page, err := jomics.url2albumPage(ALBUM_PATH, r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	zr, zf, err := loadZip(jomics.comics[album].fname)
	if err != nil {
		http.Error(w, "Error loading album file", http.StatusInternalServerError)
		return
	}
	defer zr.Close()

	numPages := len(zf)

	if page > numPages {
		http.Error(w, "Faulty URL format", http.StatusInternalServerError)
		return
	}

	data := Page{
		Title:        jomics.comics[album].title,
		First:        ALBUM_PATH + fmt.Sprintf("0x%08x/001", album),
		Last:         ALBUM_PATH + fmt.Sprintf("0x%08x/%03d", album, numPages),
		PageImageUrl: ALBUM_IMAGE_PATH + fmt.Sprintf("0x%08x/%03d", album, page),
		HasPrev:      page > 1,
		Prev:         ALBUM_PATH + fmt.Sprintf("0x%08x/%03d", album, page-1),
		HasNext:      page < numPages,
		Next:         ALBUM_PATH + fmt.Sprintf("0x%08x/%03d", album, page+1),
	}
	jomics.pageTmpl.Execute(w, data)
}

*/

func (jomics *jomics) handleFrontImage(w http.ResponseWriter, r *http.Request) {

	q := r.URL.Query()

	v := q.Get("album")

	if len(v) == 1 {
		http.Error(w, "No album given", http.StatusInternalServerError)
		return
	}

	album, err := strconv.ParseInt(v, 0, 64)
	if err != nil {
		http.Error(w, fmt.Sprintf("Unable to parse album int: %s. Error: %v\n", v, err), http.StatusInternalServerError)
		return
	}

	if c, exists := jomics.hash2comics[uint32(album)]; exists {
		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(c.frontPage)
	} else if uint32(album) == FOLDER_PNG_HASH {
		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(jomics.folder)
	} else {
		http.Error(w, "No such front page", http.StatusInternalServerError)
	}
}

type FrontPage struct {
	ComicName    string
	FrontPageUrl string
	AlbumUrl     string
}

func (jomics *jomics) handleListAlbums(w http.ResponseWriter, r *http.Request) {

	q := r.URL.Query()

	dir := jomics.home
	if v := q.Get("folder"); len(v) > 0 {

		d, err := strconv.ParseInt(v, 0, 64)
		if err != nil {
			http.Error(w, fmt.Sprintf("Unable to parse folder int: %s. Error: %v\n", v, err), http.StatusInternalServerError)
			return
		}
		dir = jomics.hash2dir[uint32(d)]
	}

	fmt.Println(dir)

	fronts := make([]FrontPage, 0, len(jomics.comics[dir]))

	for h := range jomics.comics[dir] {
		if !jomics.comics[dir][h].isDir {
			fronts = append(fronts,
				FrontPage{ComicName: jomics.comics[dir][h].title,
					FrontPageUrl: fmt.Sprintf("%s?album=0x%08x&", FRONT_IMAGE_PATH, jomics.comics[dir][h].hash),
					AlbumUrl:     fmt.Sprintf("%s?album=0x%08x&page=001", READ_PATH, jomics.comics[dir][h].hash),
				})
		} else {
			fronts = append(fronts,
				FrontPage{ComicName: jomics.comics[dir][h].title,
					FrontPageUrl: fmt.Sprintf("%s?album=0x%08x&", FRONT_IMAGE_PATH, FOLDER_PNG_HASH),
					AlbumUrl:     fmt.Sprintf("%s?folder=0x%08x&", ALBUMS_PATH, jomics.comics[dir][h].hash),
				})

		}
	}
	jomics.frontTmpl.Execute(w, fronts)
}

func faviconHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/octet-stream")
	f, _ := staticFiles.ReadFile("static/favicon.png")
	w.Write(f)
}

func folderHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/octet-stream")
	f, _ := staticFiles.ReadFile("static/folder.png")
	w.Write(f)
}

//go:embed tmpl tmpl
var tmplFiles embed.FS

//go:embed static
var staticFiles embed.FS

func createDir(dir string) (err error) {
	if stat, err := os.Stat(dir); err != nil || !stat.IsDir() {
		err = os.MkdirAll(dir, 0755)
	}
	return
}

func main() {
	var jomics jomics
	var err error

	jomics.xdg = xdg.New("gmelchett", "jomics")
	if err = createDir(jomics.xdg.CacheHome()); err != nil {
		log.Fatal("Failed to create cache directory", err)
	}

	if err = createDir(jomics.xdg.DataHome()); err != nil {
		log.Fatal("Failed to create data directory", err)
	}

	if jomics.frontPageCache, err = pudge.Open(filepath.Join(jomics.xdg.CacheHome(), JOMICS_FRONT_PAGE_CACHE),
		&pudge.Config{
			FileMode:     0644,
			DirMode:      0755,
			SyncInterval: 5,
			StoreMode:    0,
		}); err != nil {
		log.Fatal("Failed to create frontpage cache.")
	}

	jomics.listComics("/data/books/Serier/Disney/")

	jomics.loadFrontPages()

	jomics.frontPageCache.Close()

	jomics.frontTmpl = template.Must(template.ParseFS(tmplFiles, "tmpl/front.html"))
	jomics.pageTmpl = template.Must(template.ParseFS(tmplFiles, "tmpl/page.html"))

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, ALBUMS_PATH, http.StatusFound)
	})

	http.HandleFunc(ALBUMS_PATH, jomics.handleListAlbums)
	http.HandleFunc(FRONT_IMAGE_PATH, jomics.handleFrontImage)

	/*
		http.HandleFunc(ALBUM_PATH, jomics.handleShowAlbum)
		http.HandleFunc(ALBUM_IMAGE_PATH, jomics.handleAlbumImage)
		http.HandleFunc("/favicon.png", faviconHandler)
	*/

	sub, _ := fs.Sub(staticFiles, "static")

	http.Handle(STATIC_PATH, http.StripPrefix(STATIC_PATH, http.FileServer(http.FS(sub))))

	http.ListenAndServe(":8080", nil)
}
