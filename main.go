package main

import (
	"archive/zip"
	"bytes"
	"embed"
	"fmt"
	"hash/crc32"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"text/template"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp"

	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const FRONTPAGE_HEIGHT = 400
const FRONT_IMAGE_PATH = "/fronts/"
const ALBUM_PATH = "/albums/"
const ALBUM_IMAGE_PATH = "/images/"
const STATIC_PATH = "/static/"

type comic struct {
	hash      uint32
	fname     string
	title     string
	mimeType  string
	frontPage *bytes.Buffer
}

type jomic struct {
	comics       map[uint32]*comic
	sortedComics []uint32

	frontTmpl *template.Template
	pageTmpl  *template.Template
}

func (jomic *jomic) listComics(root string) {

	jomic.comics = make(map[uint32]*comic)

	s := make([]*comic, 0, 100)

	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}

		mimeType := mime.TypeByExtension(filepath.Ext(info.Name()))
		if mimeType == "application/vnd.comicbook+zip" {

			title := strings.Title(strings.ReplaceAll(info.Name()[:len(info.Name())-len(filepath.Ext(info.Name()))], "_", " "))

			fname := filepath.Join(root, info.Name())
			h := crc32.Checksum([]byte(fname), crc32.IEEETable)

			jomic.comics[h] = &comic{fname: fname,
				hash:     h,
				mimeType: mimeType,
				title:    title,
			}
			s = append(s, jomic.comics[h])
		}
		return nil
	})

	sort.Slice(s, func(i, j int) bool {
		return s[i].title < s[j].title
	})

	jomic.sortedComics = make([]uint32, len(s))
	for i := range s {
		jomic.sortedComics[i] = s[i].hash
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

func (jomic *jomic) loadFrontPages() {

	for k := range jomic.comics {
		if strings.HasSuffix(jomic.comics[k].mimeType, "zip") {
			r, imgs, err := loadZip(jomic.comics[k].fname)

			if err != nil {
				fmt.Printf("Failed to open zipfile: %s Error: %v\n", jomic.comics[k].fname, err)
				continue
			}

			fmt.Println("FrontPage:", imgs[0].Name)
			if zf, err := imgs[0].Open(); err == nil {
				if m, _, err := image.Decode(zf); err == nil {
					img := imaging.Resize(m, 0, FRONTPAGE_HEIGHT, imaging.Lanczos)

					jomic.comics[k].frontPage = new(bytes.Buffer)
					if err := jpeg.Encode(jomic.comics[k].frontPage, img, nil); err != nil {
						log.Fatalf("Failed to encode jpeg: %v\n", err)
					}

				} else {
					fmt.Printf("Failed to decode image %s in zip: %s Error: %v\n",
						imgs[0].Name, jomic.comics[k].fname, err)
				}
				zf.Close()
			} else {
				fmt.Printf("Failed to decompress %s from %s: Error: %v\n", imgs[0].Name, jomic.comics[k].fname, err)
			}
			r.Close()
		}
	}
}

func (jomic *jomic) url2albumPage(root, url string) (uint32, int, error) {

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
	if _, exists := jomic.comics[album]; !exists {
		return 0, 0, fmt.Errorf("No such album")

	}

	return album, page, nil
}

func (jomic *jomic) handleAlbumImage(w http.ResponseWriter, r *http.Request) {

	album, page, err := jomic.url2albumPage(ALBUM_IMAGE_PATH, r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	zr, zf, err := loadZip(jomic.comics[album].fname)
	if err != nil {
		http.Error(w, "Error loading album file", http.StatusInternalServerError)
		return
	}
	defer zr.Close()

	if f, err := zf[page].Open(); err == nil {
		defer f.Close()
		io.Copy(w, f)
		/*
			if b, err := f.ReadAll(); err == nil {
				w.WriteHeader(http.StatusOK)
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Write(b)
			} else {
				http.Error(w, "Decompression error", http.StatusInternalServerError)

			}
		*/
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

func (jomic *jomic) handleShowAlbum(w http.ResponseWriter, r *http.Request) {

	album, page, err := jomic.url2albumPage(ALBUM_PATH, r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	zr, zf, err := loadZip(jomic.comics[album].fname)
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
		Title:        jomic.comics[album].title,
		First:        ALBUM_PATH + fmt.Sprintf("0x%08x/001", album),
		Last:         ALBUM_PATH + fmt.Sprintf("0x%08x/%03d", album, numPages),
		PageImageUrl: ALBUM_IMAGE_PATH + fmt.Sprintf("0x%08x/%03d", album, page),
		HasPrev:      page > 1,
		Prev:         ALBUM_PATH + fmt.Sprintf("0x%08x/%03d", album, page-1),
		HasNext:      page < numPages,
		Next:         ALBUM_PATH + fmt.Sprintf("0x%08x/%03d", album, page+1),
	}
	jomic.pageTmpl.Execute(w, data)
}

func (jomic *jomic) handleFrontImage(w http.ResponseWriter, r *http.Request) {

	album, err := strconv.ParseInt(r.URL.Path[len(FRONT_IMAGE_PATH):], 0, 64)
	if err != nil {
		http.Error(w, fmt.Sprintf("Unable to int parse: %s. Error: %v\n", r.URL.Path, err), http.StatusInternalServerError)
		return
	}

	if c, exists := jomic.comics[uint32(album)]; exists {
		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(c.frontPage.Bytes())
	} else {
		http.Error(w, "No such front page", http.StatusInternalServerError)
	}
}

type FrontPage struct {
	ComicName    string
	FrontPageUrl string
	AlbumUrl     string
}

func (jomic *jomic) handleFront(w http.ResponseWriter, r *http.Request) {
	fronts := make([]FrontPage, 0, len(jomic.comics))

	for _, h := range jomic.sortedComics {
		fronts = append(fronts,
			FrontPage{ComicName: jomic.comics[h].title,
				FrontPageUrl: fmt.Sprintf("%s0x%08x", FRONT_IMAGE_PATH, h),
				AlbumUrl:     fmt.Sprintf("%s0x%08x/001", ALBUM_PATH, h),
			})
	}
	jomic.frontTmpl.Execute(w, fronts)
}

//go:embed tmpl tmpl
var tmplFiles embed.FS

//go:embed static
var staticFiles embed.FS

func main() {
	var jomic jomic

	jomic.listComics("/data/books/Serier/James Bond/")

	jomic.loadFrontPages()

	jomic.frontTmpl = template.Must(template.ParseFS(tmplFiles, "tmpl/front.html"))
	jomic.pageTmpl = template.Must(template.ParseFS(tmplFiles, "tmpl/page.html"))

	http.HandleFunc("/", jomic.handleFront)
	http.HandleFunc(FRONT_IMAGE_PATH, jomic.handleFrontImage)
	http.HandleFunc(ALBUM_PATH, jomic.handleShowAlbum)
	http.HandleFunc(ALBUM_IMAGE_PATH, jomic.handleAlbumImage)

	sub, _ := fs.Sub(staticFiles, "static")

	http.Handle(STATIC_PATH, http.StripPrefix(STATIC_PATH, http.FileServer(http.FS(sub))))

	http.ListenAndServe(":8080", nil)
}
