package main

import (
	"bytes"
	"container/list"
	"embed"
	"encoding/xml"
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
	"sort"
	"strconv"
	"text/template"

	"github.com/OpenPeeDeeP/xdg"
	"github.com/disintegration/imaging"
	"github.com/gen2brain/go-unarr"
	_ "golang.org/x/image/webp"

	"mime"
	"os"
	"path/filepath"
	"strings"
)

const THUMB_HEIGHT = 400
const FRONT_IMAGE_PATH = "/fronts/"
const READ_PATH = "/read/"
const ALBUMS_PATH = "/albums/"
const IMAGE_PATH = "/images/"
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

	files     *list.List
	comicInfo *ComicInfo
}

type jomics struct {
	home        string
	comics      map[string][]*comic
	hash2comics map[uint32]*comic
	hash2dir    map[uint32]string
	xdg         *xdg.XDG

	frontTmpl   *template.Template
	pageTmpl    *template.Template
	folder      []byte
	thumbHeight int
}

type ComicInfo struct {
	XMLName         xml.Name `xml:"ComicInfo"`
	Text            string   `xml:",chardata"`
	Xsd             string   `xml:"xsd,attr"`
	Xsi             string   `xml:"xsi,attr"`
	Title           string   `xml:"Title"`
	Series          string   `xml:"Series"`
	Number          string   `xml:"Number"`
	Volume          string   `xml:"Volume"`
	AlternateSeries string   `xml:"AlternateSeries"`
	SeriesGroup     string   `xml:"SeriesGroup"`
	Summary         string   `xml:"Summary"`
	Notes           string   `xml:"Notes"`
	Year            string   `xml:"Year"`
	Month           string   `xml:"Month"`
	Day             string   `xml:"Day"`
	Writer          string   `xml:"Writer"`
	Penciller       string   `xml:"Penciller"`
	Inker           string   `xml:"Inker"`
	Colorist        string   `xml:"Colorist"`
	Letterer        string   `xml:"Letterer"`
	CoverArtist     string   `xml:"CoverArtist"`
	Editor          string   `xml:"Editor"`
	Publisher       string   `xml:"Publisher"`
	Genre           string   `xml:"Genre"`
	Web             string   `xml:"Web"`
	PageCount       string   `xml:"PageCount"`
	LanguageISO     string   `xml:"LanguageISO"`
	AgeRating       string   `xml:"AgeRating"`
	Characters      string   `xml:"Characters"`
	Teams           string   `xml:"Teams"`
	ScanInformation string   `xml:"ScanInformation"`
	Pages           struct {
		Text string `xml:",chardata"`
		Page []struct {
			Text        string `xml:",chardata"`
			Image       string `xml:"Image,attr"`
			ImageSize   string `xml:"ImageSize,attr"`
			ImageWidth  string `xml:"ImageWidth,attr"`
			ImageHeight string `xml:"ImageHeight,attr"`
			Type        string `xml:"Type,attr"`
		} `xml:"Page"`
	} `xml:"Pages"`
}

func (jomics *jomics) listComics(root string) {

	jomics.comics = make(map[string][]*comic)
	jomics.hash2comics = make(map[uint32]*comic)
	jomics.hash2dir = make(map[uint32]string)

	filepath.WalkDir(root, func(path string, de fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			jomics.home = filepath.Dir(path)
			return nil
		}
		mt := mime.TypeByExtension(filepath.Ext(de.Name()))
		if de.IsDir() ||
			mt == "application/vnd.comicbook+zip" ||
			mt == "application/vnd.comicbook-rar" {

			hash := crc32.Checksum([]byte(path), crc32.IEEETable)
			if de.IsDir() {
				jomics.hash2dir[hash] = path
			}

			c := &comic{
				hash:  hash,
				isDir: de.IsDir(),
				fname: path,
			}

			jomics.comics[filepath.Dir(path)] = append(jomics.comics[filepath.Dir(path)], c)
			jomics.hash2comics[c.hash] = c
		}
		return nil
	})
}

func loadArchive(fname string) (*unarr.Archive, []string, error) {

	r, err := unarr.NewArchive(fname)
	if err != nil {
		return nil, nil, err
	}

	list, err := r.List()
	if err != nil {
		return nil, nil, err
	}

	imgs := make([]string, 0, len(list))

	for j := range list {
		if strings.HasPrefix(mime.TypeByExtension(filepath.Ext(list[j])), "image/") {
			imgs = append(imgs, list[j])
		}
	}

	sort.Strings(imgs)

	return r, imgs, nil
}

func (jomics *jomics) cacheFileName(hash uint32) string {
	return filepath.Join(jomics.xdg.CacheHome(), fmt.Sprintf("%08x-%d", hash, jomics.thumbHeight))
}

func (jomics *jomics) loadFromCache(hash uint32) ([]byte, bool) {
	if d, err := os.ReadFile(jomics.cacheFileName(hash)); err == nil {
		return d, true
	} else {
		return []byte{}, false
	}
}

func (jomics *jomics) saveToCache(hash uint32, data []byte) error {
	return os.WriteFile(jomics.cacheFileName(hash), data, 0644)
}

func (jomics *jomics) prepareAlbums() {

	resizeAndSave := func(h uint32, m image.Image) []byte {
		img := imaging.Resize(m, 0, jomics.thumbHeight, imaging.Lanczos)

		b := new(bytes.Buffer)
		if err := jpeg.Encode(b, img, nil); err != nil {
			log.Fatalf("Failed to encode jpeg: %v\n", err)
		}
		data := b.Bytes()
		jomics.saveToCache(h, data)
		return data
	}

	var present bool

	if jomics.folder, present = jomics.loadFromCache(FOLDER_PNG_HASH); !present {
		f, err := staticFiles.Open("static/folder.png")
		if err != nil {
			log.Fatal("Can't open folder.png - missing?", err)
		}
		m, _, err := image.Decode(f)
		if err != nil {
			log.Fatal("Can't decode folder.png - corrupt? ", err)
		}

		jomics.folder = resizeAndSave(FOLDER_PNG_HASH, m)
	}

	firstResize := false

	for i := range jomics.comics {
		for j := range jomics.comics[i] {

			f := filepath.Base(jomics.comics[i][j].fname)
			jomics.comics[i][j].title = strings.Title(strings.ReplaceAll(f[:len(f)-len(filepath.Ext(f))], "_", " "))

			if jomics.comics[i][j].isDir {
				continue
			}

			r, imgs, err := loadArchive(jomics.comics[i][j].fname)

			if err != nil {
				fmt.Printf("Failed to open zipfile: %s Error: %v\n", jomics.comics[i][j].fname, err)
				continue
			}

			if err := r.EntryFor("ComicInfo.xml"); err == nil {
				if data, err := r.ReadAll(); err == nil {

					var ci ComicInfo

					if err := xml.Unmarshal(data, &ci); err == nil {
						jomics.comics[i][j].title = ci.Series
						if len(ci.Title) > 0 {
							jomics.comics[i][j].title += " " + ci.Title
						}
						if len(ci.Number) > 0 {
							jomics.comics[i][j].title += " " + ci.Number
						}
						if len(ci.Year) > 0 {
							jomics.comics[i][j].title += " (" + ci.Year + ")"
						}

						// TODO: Sort images after page order??
						// Front cover?

						for k := range ci.Pages.Page {
							if ci.Pages.Page[k].Type == "FrontCover" {
							}
						}
					}
				}
			}

			if jomics.comics[i][j].frontPage, present = jomics.loadFromCache(jomics.comics[i][j].hash); present {
				continue
			}

			if !firstResize {
				fmt.Printf("Loading & resizing front page: ")
			}
			firstResize = true

			fmt.Printf(".")

			if err := r.EntryFor(imgs[0]); err == nil {
				data, err := r.ReadAll()
				if err != nil {
					fmt.Printf("Failed read %s from %s: %v\n", imgs[0], jomics.comics[i][j].fname, err)
					continue
				}

				if m, _, err := image.Decode(bytes.NewReader(data)); err == nil {
					jomics.comics[i][j].frontPage = resizeAndSave(jomics.comics[i][j].hash, m)
				} else {
					fmt.Printf("Failed to decode image %s in zip: %s Error: %v\n",
						imgs[0], jomics.comics[i][j].fname, err)
				}
			} else {
				fmt.Printf("Failed to decompress %s from %s: Error: %v\n", imgs[0], jomics.comics[i][j].fname, err)
			}
			r.Close()
		}
	}
	if firstResize {
		fmt.Println()
	}
}

func getAlbumPage(w http.ResponseWriter, r *http.Request) (uint32, int, error) {
	q := r.URL.Query()

	album, err := strconv.ParseInt(q.Get("album"), 0, 64)
	if err != nil {
		http.Error(w, fmt.Sprintf("Unable to parse album id: %v. Error: %v\n", q.Get("album"), err), http.StatusInternalServerError)
		return 0, 0, err
	}

	page, err := strconv.ParseInt(q.Get("page"), 10, 64)
	if err != nil {
		http.Error(w, fmt.Sprintf("Unable to parse page number: %v. Error: %v\n", q.Get("page"), err), http.StatusInternalServerError)
		return 0, 0, err
	}
	return uint32(album), int(page), nil
}

func (jomics *jomics) handleAlbumImage(w http.ResponseWriter, r *http.Request) {

	album, page, err := getAlbumPage(w, r)
	if err != nil {
		return
	}

	zr, zf, err := loadArchive(jomics.hash2comics[album].fname)
	if err != nil {
		http.Error(w, "Error loading album file", http.StatusInternalServerError)
		return
	}
	defer zr.Close()

	if page >= len(zf) {
		http.Error(w, "Page not found", http.StatusInternalServerError)
		return
	}

	if err := zr.EntryFor(zf[page]); err == nil {
		if data, err := zr.ReadAll(); err == nil {
			io.Copy(w, bytes.NewReader(data))
		} else {
			http.Error(w, "Failed to decompress page in compressed file", http.StatusInternalServerError)
		}

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

func (jomics *jomics) handleReadAlbum(w http.ResponseWriter, r *http.Request) {
	album, page, err := getAlbumPage(w, r)
	if err != nil {
		return
	}
	zr, zf, err := loadArchive(jomics.hash2comics[album].fname)
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
		Title:        jomics.hash2comics[album].title,
		First:        READ_PATH + fmt.Sprintf("?album=0x%08x&page=001", album),
		Last:         READ_PATH + fmt.Sprintf("?album=0x%08x&page=%03d", album, numPages),
		PageImageUrl: IMAGE_PATH + fmt.Sprintf("?album=0x%08x&page=%03d", album, page),
		HasPrev:      page > 1,
		Prev:         READ_PATH + fmt.Sprintf("?album=0x%08x&page=%03d", album, page-1),
		HasNext:      page < numPages,
		Next:         READ_PATH + fmt.Sprintf("?album=0x%08x&page=%03d", album, page+1),
	}
	jomics.pageTmpl.Execute(w, data)
}

func (jomics *jomics) handleFrontImage(w http.ResponseWriter, r *http.Request) {

	q := r.URL.Query()

	album, err := strconv.ParseInt(q.Get("album"), 0, 64)
	if err != nil {
		http.Error(w, fmt.Sprintf("Unable to parse album int: %v. Error: %v\n", q.Get("album"), err), http.StatusInternalServerError)
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
			http.Error(w, fmt.Sprintf("Unable to parse folder number: %v. Error: %v\n", v, err), http.StatusInternalServerError)
			return
		}
		dir = jomics.hash2dir[uint32(d)]
	}

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
	jomics := jomics{
		thumbHeight: THUMB_HEIGHT,
	}

	var err error

	jomics.xdg = xdg.New("gmelchett", "jomics")
	if err = createDir(jomics.xdg.CacheHome()); err != nil {
		log.Fatal("Failed to create cache directory", err)
	}

	if err = createDir(jomics.xdg.DataHome()); err != nil {
		log.Fatal("Failed to create data directory", err)
	}

	jomics.listComics("/data/books/Serier/Disney/")

	jomics.prepareAlbums()

	jomics.frontTmpl = template.Must(template.ParseFS(tmplFiles, "tmpl/front.html"))
	jomics.pageTmpl = template.Must(template.ParseFS(tmplFiles, "tmpl/page.html"))

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, ALBUMS_PATH, http.StatusFound)
	})

	http.HandleFunc(ALBUMS_PATH, jomics.handleListAlbums)
	http.HandleFunc(FRONT_IMAGE_PATH, jomics.handleFrontImage)
	http.HandleFunc(READ_PATH, jomics.handleReadAlbum)
	http.HandleFunc(IMAGE_PATH, jomics.handleAlbumImage)

	http.HandleFunc("/favicon.png", faviconHandler)

	sub, _ := fs.Sub(staticFiles, "static")

	http.Handle(STATIC_PATH, http.StripPrefix(STATIC_PATH, http.FileServer(http.FS(sub))))

	http.ListenAndServe(":8080", nil)
}
