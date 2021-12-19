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

type jomics struct {
	comics       map[uint32]*comic
	sortedComics []uint32

	frontTmpl *template.Template
	pageTmpl  *template.Template
}

func (jomics *jomics) listComics(root string) {

	jomics.comics = make(map[uint32]*comic)

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

			jomics.comics[h] = &comic{fname: fname,
				hash:     h,
				mimeType: mimeType,
				title:    title,
			}
			s = append(s, jomics.comics[h])
		}
		return nil
	})

	sort.Slice(s, func(i, j int) bool {
		return s[i].title < s[j].title
	})

	jomics.sortedComics = make([]uint32, len(s))
	for i := range s {
		jomics.sortedComics[i] = s[i].hash
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

	for k := range jomics.comics {
		if strings.HasSuffix(jomics.comics[k].mimeType, "zip") {
			r, imgs, err := loadZip(jomics.comics[k].fname)

			if err != nil {
				fmt.Printf("Failed to open zipfile: %s Error: %v\n", jomics.comics[k].fname, err)
				continue
			}

			fmt.Println("FrontPage:", imgs[0].Name)
			if zf, err := imgs[0].Open(); err == nil {
				if m, _, err := image.Decode(zf); err == nil {
					img := imaging.Resize(m, 0, FRONTPAGE_HEIGHT, imaging.Lanczos)

					jomics.comics[k].frontPage = new(bytes.Buffer)
					if err := jpeg.Encode(jomics.comics[k].frontPage, img, nil); err != nil {
						log.Fatalf("Failed to encode jpeg: %v\n", err)
					}

				} else {
					fmt.Printf("Failed to decode image %s in zip: %s Error: %v\n",
						imgs[0].Name, jomics.comics[k].fname, err)
				}
				zf.Close()
			} else {
				fmt.Printf("Failed to decompress %s from %s: Error: %v\n", imgs[0].Name, jomics.comics[k].fname, err)
			}
			r.Close()
		}
	}
}

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

func (jomics *jomics) handleFrontImage(w http.ResponseWriter, r *http.Request) {

	album, err := strconv.ParseInt(r.URL.Path[len(FRONT_IMAGE_PATH):], 0, 64)
	if err != nil {
		http.Error(w, fmt.Sprintf("Unable to int parse: %s. Error: %v\n", r.URL.Path, err), http.StatusInternalServerError)
		return
	}

	if c, exists := jomics.comics[uint32(album)]; exists {
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

func (jomics *jomics) handleFront(w http.ResponseWriter, r *http.Request) {
	fronts := make([]FrontPage, 0, len(jomics.comics))

	for _, h := range jomics.sortedComics {
		fronts = append(fronts,
			FrontPage{ComicName: jomics.comics[h].title,
				FrontPageUrl: fmt.Sprintf("%s0x%08x", FRONT_IMAGE_PATH, h),
				AlbumUrl:     fmt.Sprintf("%s0x%08x/001", ALBUM_PATH, h),
			})
	}
	jomics.frontTmpl.Execute(w, fronts)
}

func faviconHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/octet-stream")
	f, _ := staticFiles.ReadFile("static/favicon.png")
	w.Write(f)
}

//go:embed tmpl tmpl
var tmplFiles embed.FS

//go:embed static
var staticFiles embed.FS

func main() {
	var jomics jomics

	jomics.listComics("/data/books/Serier/James Bond/")

	jomics.loadFrontPages()

	jomics.frontTmpl = template.Must(template.ParseFS(tmplFiles, "tmpl/front.html"))
	jomics.pageTmpl = template.Must(template.ParseFS(tmplFiles, "tmpl/page.html"))

	http.HandleFunc("/", jomics.handleFront)
	http.HandleFunc(FRONT_IMAGE_PATH, jomics.handleFrontImage)
	http.HandleFunc(ALBUM_PATH, jomics.handleShowAlbum)
	http.HandleFunc(ALBUM_IMAGE_PATH, jomics.handleAlbumImage)
	http.HandleFunc("/favicon.png", faviconHandler)

	sub, _ := fs.Sub(staticFiles, "static")

	http.Handle(STATIC_PATH, http.StripPrefix(STATIC_PATH, http.FileServer(http.FS(sub))))

	http.ListenAndServe(":8080", nil)
}
