package main

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/boltdb/bolt"

	blackfriday "gopkg.in/russross/blackfriday.v2"
)

var db *bolt.DB

var validPath = regexp.MustCompile(`^/(edit|save|view|history)/(.*)|login$`)

// after making a change to template files, you need to run go generate.
// it will apply the changes to gen_bakego.go
//
// the generated code will be used when a user initializing
// his/her wiki first time.
//
//go:generate bakego -d tmpl

var templates *template.Template

type Page struct {
	Title   string
	Body    []byte
	Created time.Time
	Author  string
}

func (p *Page) HTML() template.HTML {
	return template.HTML(blackfriday.Run(p.Body))
}

type HistoryPage struct {
	Title string
	Revs  []Revision
}

type Revision struct {
	Num     int
	Created time.Time
	Author  string
}

type LogInPage struct {
	Title string
}

func byteID(id uint64) []byte {
	bid := make([]byte, 8)
	binary.BigEndian.PutUint64(bid, id)
	return bid
}

func toBytes(x interface{}) []byte {
	buf := &bytes.Buffer{}
	enc := gob.NewEncoder(buf)
	enc.Encode(x)
	return buf.Bytes()
}

func fromBytes(bs []byte, x interface{}) {
	buf := bytes.NewBuffer(bs)
	dec := gob.NewDecoder(buf)
	dec.Decode(x)
}

func savePage(p *Page) error {
	pageBytes := toBytes(p)
	return db.Update(func(tx *bolt.Tx) error {
		b, err := tx.Bucket([]byte("history")).CreateBucketIfNotExists([]byte(p.Title))
		if err != nil {
			return fmt.Errorf("could not create bucket: %s", err)
		}
		id, _ := b.NextSequence()
		if err := b.Put(byteID(id), pageBytes); err != nil {
			return err
		}
		return nil
	})
}

func loadPage(title string) (*Page, error) {
	return loadPageRev(title, 0)
}

func loadPageRev(title string, id uint64) (*Page, error) {
	var pageBytes []byte
	err := db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("history")).Bucket([]byte(title))
		if b == nil {
			return errors.New("page not exists")
		}
		if id == 0 {
			// bolt's id creator (Bucket.NextSequence) create ids from 1,
			// I will treat 0 as latest revision.
			c := b.Cursor()
			_, pageBytes = c.Last()
		} else {
			pageBytes = b.Get(byteID(id))
		}
		if pageBytes == nil {
			return errors.New("page not exists")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	page := &Page{}
	fromBytes(pageBytes, page)
	return page, nil
}

func makeRootHandler(homePage string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/view/"+homePage, http.StatusFound)
			return
		} else if r.URL.Path == "/login" {
			http.Redirect(w, r, "/view/"+homePage+"?login=1", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	}
}

func makeHandler(fn func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := validPath.FindStringSubmatch(r.URL.Path)
		if m == nil {
			http.NotFound(w, r)
			return
		}
		if login := r.URL.Query().Get("login"); login != "" {
			loginHandler(w, r, m[2])
			return
		}
		if signup := r.URL.Query().Get("signup"); signup != "" {
			signupHandler(w, r, m[2])
			return
		}
		fn(w, r, m[2])
	}
}

func loginHandler(w http.ResponseWriter, r *http.Request, title string) {
	renderTemplate(w, "login", &LogInPage{Title: title})
}

func signupHandler(w http.ResponseWriter, r *http.Request, title string) {
	renderTemplate(w, "signup", &LogInPage{Title: title})
}

func viewHandler(w http.ResponseWriter, r *http.Request, title string) {
	if rev := r.URL.Query().Get("rev"); rev != "" {
		id, err := strconv.ParseUint(rev, 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		p, err := loadPageRev(title, id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		renderTemplate(w, "view", p)
		return
	}
	p, err := loadPage(title)
	if err != nil {
		http.Redirect(w, r, "/edit/"+title, http.StatusFound)
		return
	}
	renderTemplate(w, "view", p)
}

func editHandler(w http.ResponseWriter, r *http.Request, title string) {
	p, err := loadPage(title)
	if err != nil {
		p = &Page{Title: title}
	}
	renderTemplate(w, "edit", p)
}

func saveHandler(w http.ResponseWriter, r *http.Request, title string) {
	body := strings.Replace(r.FormValue("body"), "\r\n", "\n", -1)
	p := &Page{Title: title, Body: []byte(body), Created: time.Now(), Author: r.RemoteAddr}
	err := savePage(p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	http.Redirect(w, r, "/view/"+title, http.StatusFound)
}

func historyHandler(w http.ResponseWriter, r *http.Request, title string) {
	froms := r.URL.Query().Get("from")
	from, err := strconv.Atoi(froms)
	if err != nil {
		from = -1
	}
	h, err := loadHistory(title, from, 20)
	if err != nil {
		h = &HistoryPage{Title: title}
	}
	renderTemplate(w, "history", h)
}

func loadHistory(title string, from, n int) (*HistoryPage, error) {
	h := &HistoryPage{Title: title}
	err := db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("history")).Bucket([]byte(title))
		if b == nil {
			return errors.New("page not exists")
		}
		c := b.Cursor()

		var (
			k []byte
			v []byte
			p = &Page{}
		)
		if from == -1 {
			k, v = c.Last()
			if k == nil {
				return errors.New("page not exists")
			}
		} else {
			idb := make([]byte, 8)
			binary.BigEndian.PutUint64(idb, uint64(from))
			k, v = c.Seek(idb)
			if bytes.Compare(k, idb) != 0 {
				return errors.New("page not exists")
			}
		}
		i := 0
		for ; k != nil; k, v = c.Prev() {
			// first iteration's k, v come from outside of this loop.
			if i >= n {
				break
			}
			fromBytes(v, p)
			h.Revs = append(h.Revs, Revision{Num: int(binary.BigEndian.Uint64(k)), Created: p.Created, Author: p.Author})
			i++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return h, nil
}

func redirectToHttps(w http.ResponseWriter, r *http.Request) {
	to := "https://" + strings.Split(r.Host, ":")[0] + r.URL.Path
	if r.URL.RawQuery != "" {
		to += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, to, http.StatusTemporaryRedirect)
}

func renderTemplate(w http.ResponseWriter, tmpl string, p interface{}) {
	err := templates.ExecuteTemplate(w, tmpl+".html", p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func main() {
	var (
		init     bool
		addr     string
		https    bool
		key      string
		cert     string
		homePage string
	)

	flag.BoolVar(&init, "init", false, "intialize whisky dir. it ignores other flags")
	flag.StringVar(&homePage, "home", "Home", "homepage of the wiki")
	flag.StringVar(&addr, "addr", ":8080", "binding address")
	flag.BoolVar(&https, "https", false, "turn on https at 443")
	flag.StringVar(&cert, "cert", "", "https cert file")
	flag.StringVar(&key, "key", "", "https key file")
	flag.Parse()

	if init {
		err := bakego.Extract()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	} else {
		err := bakego.Ensure()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\ndid you initialized whisky with -init flag?\n", err)
			os.Exit(1)
		}
	}

	templates = template.Must(template.ParseGlob("tmpl/*.html"))

	if https && (cert == "" || key == "") {
		fmt.Fprintln(os.Stderr, "https flag needs both cert and key flags")
		os.Exit(1)
	}

	var err error
	db, err = bolt.Open("whisky.db", 0600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	err = db.Update(func(tx *bolt.Tx) error {
		for _, buc := range []string{"history"} {
			_, err := tx.CreateBucketIfNotExists([]byte(buc))
			if err != nil {
				return fmt.Errorf("create buckets: %s", err)
			}
		}
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", makeRootHandler(homePage))
	mux.HandleFunc("/view/", makeHandler(viewHandler))
	mux.HandleFunc("/edit/", makeHandler(editHandler))
	mux.HandleFunc("/save/", makeHandler(saveHandler))
	mux.HandleFunc("/history/", makeHandler(historyHandler))

	if https {
		go func() {
			log.Fatal(http.ListenAndServe(addr, http.HandlerFunc(redirectToHttps)))
		}()
		httpsAddr := strings.Split(addr, ":")[0] + ":443"
		log.Fatal(http.ListenAndServeTLS(httpsAddr, cert, key, mux))
	} else {
		log.Fatal(http.ListenAndServe(addr, mux))
	}
}
