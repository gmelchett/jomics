package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"net/http"
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

type comic struct {
	fname     string
	title     string
	mimeType  string
	frontPage *bytes.Buffer
}

type jomic struct {
	comics    []*comic
	frontTmpl *template.Template
}

func listComics(root string) []*comic {

	var comics []*comic

	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}

		mimeType := mime.TypeByExtension(filepath.Ext(info.Name()))
		if mimeType == "application/vnd.comicbook+zip" {

			title := strings.Title(strings.ReplaceAll(info.Name()[:len(info.Name())-len(filepath.Ext(info.Name()))], "_", " "))

			comics = append(comics,
				&comic{fname: filepath.Join(root, info.Name()),
					mimeType: mimeType,
					title:    title,
				})
		}
		return nil
	})
	return comics
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

func loadFrontPages(comics []*comic) {

	for i := range comics {
		if strings.HasSuffix(comics[i].mimeType, "zip") {
			r, imgs, err := loadZip(comics[i].fname)

			if err != nil {
				fmt.Printf("Failed to open zipfile: %s Error: %v\n", comics[i].fname, err)
				continue
			}

			fmt.Println("FrontPage:", imgs[0].Name)
			if zf, err := imgs[0].Open(); err == nil {
				if m, _, err := image.Decode(zf); err == nil {
					img := imaging.Resize(m, 0, FRONTPAGE_HEIGHT, imaging.Lanczos)

					comics[i].frontPage = new(bytes.Buffer)
					if err := jpeg.Encode(comics[i].frontPage, img, nil); err != nil {
						log.Fatalf("Failed to encode jpeg: %v\n", err)
					}

				} else {
					fmt.Printf("Failed to decode image %s in zip: %s Error: %v\n",
						imgs[0].Name, comics[i].fname, err)
				}
				zf.Close()
			} else {
				fmt.Printf("Failed to decompress %s from %s: Error: %v\n", imgs[0].Name, comics[i].fname, err)
			}
			r.Close()
		}
	}
}

type FrontPage struct {
	ComicName    string
	FrontPageUrl string
	AlbumUrl     string
}

func (jomic *jomic) handleFrontImage(w http.ResponseWriter, r *http.Request) {

	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(jomic.comics[0].frontPage.Bytes())
}

func (jomic *jomic) handleFront(w http.ResponseWriter, r *http.Request) {
	fronts := make([]FrontPage, 0, len(jomic.comics))

	for i := range jomic.comics {
		fronts = append(fronts,
			FrontPage{ComicName: jomic.comics[i].title, FrontPageUrl: fmt.Sprintf("/fronts/%03d", i), AlbumUrl: "/albums/asdf"})
	}
	jomic.frontTmpl.Execute(w, fronts)
}

func main() {
	var jomic jomic

	jomic.comics = listComics("/data/books/Serier/James Bond/")

	loadFrontPages(jomic.comics)

	jomic.frontTmpl = template.Must(template.ParseFiles("tmpl/front.html"))

	http.HandleFunc("/", jomic.handleFront)
	http.HandleFunc("/fronts/", jomic.handleFrontImage)

	http.ListenAndServe(":8080", nil)
}
