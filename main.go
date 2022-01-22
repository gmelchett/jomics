//
// SPDX-FileCopyrightText: 2021 Jonas Aaberg
//
// SPDX-License-Identifier: MIT
//

package main

import (
	"archive/zip"
	"bytes"
	"embed"
	"encoding/xml"
	"flag"
	"fmt"
	"hash/crc32"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/jpeg"
	"image/png"
	_ "image/png"
	"io"
	"io/fs"
	"log"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"text/template"
	"time"

	"gerace.dev/zipfs"
	"github.com/OpenPeeDeeP/xdg"
	"github.com/disintegration/imaging"
	"github.com/gen2brain/go-unarr"
	_ "golang.org/x/image/webp"

	"os"
	"path/filepath"
	"strings"
)

const THUMB_HEIGHT = 400
const FRONT_COVER_PATH = "/covers/"
const READ_PATH = "/read/"
const ALBUMS_PATH = "/albums/"
const IMAGE_PATH = "/images/"
const STATIC_PATH = "/static/"
const CSS_PATH = "/css/"

var FOLDER_PNG_HASH = crc32.Checksum([]byte("folder.png"), crc32.IEEETable)

type comic struct {
	hash       uint32
	fname      string
	title      string
	isDir      bool
	frontCover []byte

	comicInfo *ComicInfo
}

type comicCollection struct {
	cacheDir    string
	thumbHeight int
	quiet       bool
	comics      map[string][]*comic
	hash2comics map[uint32]*comic
	hash2dir    map[uint32]string
}

type jomics struct {
	theme      string
	rootDir    string
	webroot    string
	collection *comicCollection
	colMutex   sync.Mutex

	xdg *xdg.XDG

	frontCoverTmpl *template.Template
	pageTmpl       *template.Template
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

func (col *comicCollection) listComics(root string) {

	filepath.WalkDir(root, func(path string, de fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}

		if de.IsDir() || filepath.Ext(de.Name()) == ".cbz" || filepath.Ext(de.Name()) == ".cbr" {
			hash := crc32.Checksum([]byte(path), crc32.IEEETable)
			if de.IsDir() {
				col.hash2dir[hash] = path
			}

			c := &comic{
				hash:  hash,
				isDir: de.IsDir(),
				fname: path,
			}

			col.comics[filepath.Dir(path)] = append(col.comics[filepath.Dir(path)], c)
			col.hash2comics[c.hash] = c
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
		ext := strings.ToLower(filepath.Ext(list[j]))
		if ext == ".jpg" || ext == ".png" || ext == ".webp" || ext == ".gif" || ext == ".jpeg" {
			imgs = append(imgs, list[j])
		}
	}

	sort.Strings(imgs)

	return r, imgs, nil
}

func (col *comicCollection) cacheFileName(hash uint32) string {
	return filepath.Join(col.cacheDir, fmt.Sprintf("%08x-%d", hash, col.thumbHeight))
}

func (col *comicCollection) loadFromCache(hash uint32) ([]byte, bool) {
	if d, err := os.ReadFile(col.cacheFileName(hash)); err == nil {
		return d, true
	} else {
		return []byte{}, false
	}
}

func (col *comicCollection) saveToCache(hash uint32, data []byte) error {
	return os.WriteFile(col.cacheFileName(hash), data, 0644)
}

func (col *comicCollection) prepareAlbums() {

	resizeAndSave := func(h uint32, m image.Image, toPng bool) []byte {
		img := imaging.Resize(m, 0, col.thumbHeight, imaging.Lanczos)

		b := new(bytes.Buffer)
		var err error
		if !toPng {
			err = jpeg.Encode(b, img, nil)
		} else {
			err = png.Encode(b, img)
		}
		if err != nil {
			log.Fatalf("Failed to encode image: %v\n", err)
		}

		data := b.Bytes()
		col.saveToCache(h, data)
		return data
	}

	var present bool

	folder, present := col.loadFromCache(FOLDER_PNG_HASH)
	if !present {
		f, err := staticFiles.Open("static/folder.png")
		if err != nil {
			log.Fatal("Can't open folder.png - missing?", err)
		}
		m, _, err := image.Decode(f)
		if err != nil {
			log.Fatal("Can't decode folder.png - corrupt? ", err)
		}

		folder = resizeAndSave(FOLDER_PNG_HASH, m, true)
	}
	col.hash2comics[FOLDER_PNG_HASH] = &comic{frontCover: folder}

	firstResize := false

	for i := range col.comics {
		for j := range col.comics[i] {

			f := filepath.Base(col.comics[i][j].fname)
			col.comics[i][j].title = strings.Title(strings.ReplaceAll(f[:len(f)-len(filepath.Ext(f))], "_", " "))

			if col.comics[i][j].isDir {
				continue
			}

			r, imgs, err := loadArchive(col.comics[i][j].fname)

			if err != nil {
				fmt.Printf("Failed to open zipfile: %s Error: %v\n", col.comics[i][j].fname, err)
				continue
			}

			if err := r.EntryFor("ComicInfo.xml"); err == nil {
				if data, err := r.ReadAll(); err == nil {

					var ci ComicInfo

					if err := xml.Unmarshal(data, &ci); err == nil {
						col.comics[i][j].title = ci.Series
						if len(ci.Title) > 0 {
							col.comics[i][j].title += " " + ci.Title
						}
						if len(ci.Number) > 0 {
							col.comics[i][j].title += " " + ci.Number
						}
						if len(ci.Year) > 0 {
							col.comics[i][j].title += " (" + ci.Year + ")"
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

			if col.comics[i][j].frontCover, present = col.loadFromCache(col.comics[i][j].hash); present {
				r.Close()
				continue
			}

			if !firstResize && !col.quiet {
				fmt.Printf("Load & resize front pages")
			}
			firstResize = true

			if !col.quiet {
				fmt.Printf(".")
			}

			if len(imgs) == 0 {
				fmt.Printf("\nWarning: No images found in: %s\n", col.comics[i][j].fname)
				r.Close()
				continue
			}

			if err := r.EntryFor(imgs[0]); err == nil {
				data, err := r.ReadAll()
				if err != nil {
					fmt.Printf("\nFailed read %s from %s: %v\n", imgs[0], col.comics[i][j].fname, err)
					r.Close()
					continue
				}

				if m, _, err := image.Decode(bytes.NewReader(data)); err == nil {
					col.comics[i][j].frontCover = resizeAndSave(col.comics[i][j].hash, m, false)
				} else {
					fmt.Printf("\nFailed to decode image %s in zip: %s Error: %v\n",
						imgs[0], col.comics[i][j].fname, err)
				}
			} else {
				fmt.Printf("\nFailed to decompress %s from %s: Error: %v\n", imgs[0], col.comics[i][j].fname, err)
			}
			r.Close()
		}
	}
	if firstResize && !col.quiet {
		fmt.Println("done")
	}
}

func scanCollection(rootDir, cacheDir string, thumbHeight int, quiet bool) *comicCollection {
	col := &comicCollection{
		comics:      make(map[string][]*comic),
		hash2comics: make(map[uint32]*comic),
		hash2dir:    make(map[uint32]string),
		cacheDir:    cacheDir,
		thumbHeight: thumbHeight,
		quiet:       quiet,
	}
	col.listComics(rootDir)
	col.prepareAlbums()
	return col
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

	jomics.colMutex.Lock()
	defer jomics.colMutex.Unlock()

	zr, zf, err := loadArchive(jomics.collection.hash2comics[album].fname)
	if err != nil {
		http.Error(w, "Error loading album file", http.StatusInternalServerError)
		return
	}
	defer zr.Close()

	if page >= len(zf) || page < 0 {
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
	Theme        string
	Title        string
	WebRoot      string
	Page         int
	NumPages     int
	First        string
	Back         string
	Last         string
	PageImageUrl string
	Prev         string
	PrevDisabled string
	HasNext      bool
	NextDisabled string
	Next         string
}

func (jomics *jomics) handleReadAlbum(w http.ResponseWriter, r *http.Request) {

	album, page, err := getAlbumPage(w, r)
	if err != nil {
		return
	}

	jomics.colMutex.Lock()
	defer jomics.colMutex.Unlock()

	zr, zf, err := loadArchive(jomics.collection.hash2comics[album].fname)
	if err != nil {
		http.Error(w, "Error loading album file", http.StatusInternalServerError)
		return
	}
	defer zr.Close()

	numPages := len(zf)

	if page >= numPages || page < 0 {
		http.Error(w, "Faulty URL format", http.StatusInternalServerError)
		return
	}
	backFolder := "/"
	folder := 0x0

	if f, err := strconv.ParseInt(r.URL.Query().Get("folder"), 0, 64); err == nil {
		backFolder = ALBUMS_PATH + fmt.Sprintf("?folder=0x%08x", f)
		folder = int(f)
	}

	af := fmt.Sprintf("?album=0x%08x&folder=0x%08x&", album, folder)

	data := Page{
		Theme:        jomics.theme,
		Title:        jomics.collection.hash2comics[album].title,
		Page:         page + 1,
		NumPages:     numPages,
		First:        READ_PATH + af + "page=0",
		Back:         backFolder,
		Last:         READ_PATH + af + fmt.Sprintf("page=%d", numPages-1),
		WebRoot:      jomics.webroot,
		PageImageUrl: IMAGE_PATH + af + fmt.Sprintf("page=%d", page),
		Prev:         READ_PATH + af + fmt.Sprintf("page=%d", page-1),
		HasNext:      page < (numPages - 1),
		Next:         READ_PATH + af + fmt.Sprintf("page=%d", page+1),
	}
	if page == 0 {
		data.PrevDisabled = "disabled"
	}
	if !data.HasNext {
		data.NextDisabled = "disabled"
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

	jomics.colMutex.Lock()
	defer jomics.colMutex.Unlock()

	if c, exists := jomics.collection.hash2comics[uint32(album)]; exists {
		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(c.frontCover)
	} else {
		http.Error(w, "No such front page", http.StatusInternalServerError)
	}
}

type FrontCover struct {
	ComicName     string
	FrontCoverUrl string
	AlbumUrl      string
	WebRoot       string
}

type Albums struct {
	WebRoot     string
	Theme       string
	Logo        string
	FrontCovers []FrontCover
}

func (jomics *jomics) handleListAlbums(w http.ResponseWriter, r *http.Request) {

	jomics.colMutex.Lock()
	defer jomics.colMutex.Unlock()

	dir := jomics.rootDir
	folderHash := 0

	if d, err := strconv.ParseInt(r.URL.Query().Get("folder"), 0, 64); err == nil {
		if v, exists := jomics.collection.hash2dir[uint32(d)]; exists {
			folderHash = int(d)
			dir = v
		}
	}

	albums := Albums{
		Theme:       jomics.theme,
		Logo:        "jomics.png",
		WebRoot:     jomics.webroot,
		FrontCovers: make([]FrontCover, 0, len(jomics.collection.comics[dir])),
	}

	if jomics.theme == "dark" {
		albums.Logo = "jomics-dark.png"
	}

	for h := range jomics.collection.comics[dir] {
		if !jomics.collection.comics[dir][h].isDir {
			albums.FrontCovers = append(albums.FrontCovers,
				FrontCover{ComicName: jomics.collection.comics[dir][h].title,
					FrontCoverUrl: fmt.Sprintf("%s?album=0x%08x&", FRONT_COVER_PATH, jomics.collection.comics[dir][h].hash),
					AlbumUrl:      fmt.Sprintf("%s?folder=0x%08x&album=0x%08x&page=0", READ_PATH, folderHash, jomics.collection.comics[dir][h].hash),
					WebRoot:       jomics.webroot,
				})
		} else {
			albums.FrontCovers = append(albums.FrontCovers,
				FrontCover{ComicName: jomics.collection.comics[dir][h].title,
					FrontCoverUrl: fmt.Sprintf("%s?album=0x%08x&", FRONT_COVER_PATH, FOLDER_PNG_HASH),
					AlbumUrl:      fmt.Sprintf("%s?folder=0x%08x&", ALBUMS_PATH, jomics.collection.comics[dir][h].hash),
					WebRoot:       jomics.webroot,
				})

		}
	}
	jomics.frontCoverTmpl.Execute(w, albums)
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

//go:embed css/pico-master.zip
var picocssZipFile []byte

func createDir(dir string) (err error) {
	if stat, err := os.Stat(dir); err != nil || !stat.IsDir() {
		err = os.MkdirAll(dir, 0755)
	}
	return
}

func main() {

	var thumbHeight = flag.Int("th", THUMB_HEIGHT, "Front page thumb nail size.")
	var root = flag.String("root", "", "Comic collection root.")
	var addr = flag.String("addr", "localhost:4531", "Server address.\nSet to \":4531\" if you want jomics to be reachable from other computers.")
	var webroot = flag.String("webroot", "", "For reverse proxy servers use.")
	var scanInter = flag.Int("si", 300, "Rescan collection interval. Zero or negative to disable.")
	var quiet = flag.Bool("q", false, "Quiet. No prints when new comics are discovered.")

	var light = flag.Bool("light", false, "Light CSS theme. Default is dark.")

	flag.Parse()

	if len(*root) == 0 {
		fmt.Println("You must provide a root directory.")
		os.Exit(1)
	}

	if *thumbHeight < 100 || *thumbHeight > 2000 {
		fmt.Println("Invalid thumbnail height.")
		os.Exit(1)
	}

	*webroot = strings.TrimRight(*webroot, "/")

	jomics := jomics{
		theme:   "dark",
		webroot: *webroot,
		rootDir: filepath.Dir(*root),
	}

	if *light {
		jomics.theme = "light"
	}

	var err error

	jomics.xdg = xdg.New("gmelchett", "jomics")
	if err = createDir(jomics.xdg.CacheHome()); err != nil {
		log.Fatal("Failed to create cache directory", err)
	}

	jomics.collection = scanCollection(*root, jomics.xdg.CacheHome(), *thumbHeight, *quiet)

	jomics.frontCoverTmpl = template.Must(template.ParseFS(tmplFiles, "tmpl/frontcover.html"))
	jomics.pageTmpl = template.Must(template.ParseFS(tmplFiles, "tmpl/page.html"))

	picocssZipReader, err := zip.NewReader(bytes.NewReader(picocssZipFile), int64(len(picocssZipFile)))
	if err != nil {
		log.Fatalf("pico-master.zip is faulty: %v", err)
	}

	picocssZipFs, err := zipfs.NewZipFileSystem(picocssZipReader)
	if err != nil {
		log.Fatalf("zipfs creation failure: %v", err)
	}

	http.HandleFunc(*webroot+"/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, *webroot+ALBUMS_PATH, http.StatusFound)
	})

	http.HandleFunc(*webroot+ALBUMS_PATH, jomics.handleListAlbums)
	http.HandleFunc(*webroot+FRONT_COVER_PATH, jomics.handleFrontImage)
	http.HandleFunc(*webroot+READ_PATH, jomics.handleReadAlbum)
	http.HandleFunc(*webroot+IMAGE_PATH, jomics.handleAlbumImage)

	http.HandleFunc(*webroot+"/favicon.png", faviconHandler)

	sub, _ := fs.Sub(staticFiles, "static")

	http.Handle(*webroot+STATIC_PATH, http.StripPrefix(*webroot+STATIC_PATH, http.FileServer(http.FS(sub))))
	http.Handle(*webroot+CSS_PATH, http.StripPrefix(*webroot+CSS_PATH, http.FileServer(picocssZipFs)))

	if *scanInter > 0 {
		go func() {
			for {
				time.Sleep(time.Duration(*scanInter) * time.Second)
				c := scanCollection(*root, jomics.xdg.CacheHome(), *thumbHeight, *quiet)
				jomics.colMutex.Lock()
				jomics.collection = c
				jomics.colMutex.Unlock()
			}
		}()
	}

	if err := http.ListenAndServe(*addr, nil); err != nil {
		log.Fatal("Failed to start server. Probably faulty address.", err)
	}
}
