package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/gorilla/mux"
	mcassoc "github.com/lukegb/mcassoc/mcassoc"
	minecraft "github.com/lukegb/mcassoc/minecraft"
	mojang "github.com/lukegb/mcassoc/mojang"
	"html/template"
	"image/png"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"time"
)

var sesskey []byte
var authenticator mcassoc.Associfier
var httplistenloc string

type TemplatePageData struct {
	Title string
}

type TemplateData struct {
	PageData TemplatePageData
	Data     interface{}
}

type SigningData struct {
	Username string `json:"username"`
	UUID     string `json:"uuid"`
	Now      int64  `json:"now"`
}

func generateSharedKey(siteid string) []byte {
	z := hmac.New(sha512.New, sesskey)
	z.Write([]byte(siteid))
	key := z.Sum([]byte{})
	return key
}
func generateDataBlob(data SigningData, siteid string) string {
	skey := generateSharedKey(siteid)
	databytes, _ := json.Marshal(data)
	x := hmac.New(sha1.New, skey)
	x.Write(databytes)
	datahash := x.Sum(databytes)
	return base64.URLEncoding.EncodeToString(datahash)
}

func HomePage(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("<!DOCTYPE html><html><body><h1>Minecraft Account Association</h1><p>For access, please email lukegb: my email is (my username) AT (my username) DOT com.</p></body></html>"))
}

func PerformPage(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	siteID := v.Get("siteid")
	postbackURL := v.Get("postback")
	key := v.Get("key")
	mcuser := v.Get("mcusername")

	if pbu, err := url.Parse(postbackURL); err != nil || (pbu.Scheme != "http" && pbu.Scheme != "https") {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("postback must be a HTTP/HTTPS url"))
		return
	}

	// check that the required fields are set
	if siteID == "" || postbackURL == "" || key == "" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("required parameter(s) missing"))
		return
	}

	t := template.Must(template.ParseFiles("templates/minibase.html", "templates/perform.html"))

	t.ExecuteTemplate(w, "layout", TemplateData{
		PageData: TemplatePageData{
			Title: "Minecraft Account Association",
		},
		Data: struct {
			SiteID      string
			PostbackURL string
			Key         string
			MCUser      string
		}{
			SiteID:      siteID,
			PostbackURL: postbackURL,
			Key:         key,
			MCUser:      mcuser,
		},
	})
}

func ApiCheckUserPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		w.Write([]byte("must be a POST request"))
		return
	}

	err := r.ParseForm()
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("data invalid"))
		return
	}

	je := json.NewEncoder(w)

	mcusername := r.Form.Get("mcusername")
	// don't do anything yet

	user, err := mojang.GetProfileByUsername(mcusername)
	if err != nil {
		if err == mojang.ERR_NO_SUCH_USER {
			je.Encode(struct {
				Error string `json:"error"`
			}{
				Error: "no such user",
			})
			return
		} else {
			log.Println("error while getting mojang profile", mcusername, err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	}

	mcprofile, err := minecraft.GetProfile(user.Id)
	if err != nil {
		log.Println("error while getting minecraft profile", mcusername, user.Id, err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	skinim, err := minecraft.GetSkin(mcprofile)
	if err != nil {
		log.Println("error while getting skin", mcusername, user.Id, mcprofile, err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	je.Encode(struct {
		MCUsername string `json:"mcusername"`
		UUID       string `json:"uuid"`
		Exists     bool   `json:"exists"`
	}{
		MCUsername: mcusername,
		UUID:       user.Id,
		Exists:     mcassoc.HasDatablock(skinim),
	})
}

func ApiAuthenticateUserPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		w.Write([]byte("must be a POST request"))
		return
	}

	err := r.ParseForm()
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("data invalid"))
		return
	}

	je := json.NewEncoder(w)

	uuid := r.Form.Get("uuid")
	password := r.Form.Get("password")

	mcprofile, err := minecraft.GetProfile(uuid)
	if err != nil {
		log.Println("error while getting minecraft profile", uuid, err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	skinim, err := minecraft.GetSkin(mcprofile)
	if err != nil {
		log.Println("error while getting skin", uuid, mcprofile, err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	passwordok, err := authenticator.Verify(password, skinim)
	if err != nil {
		log.Println("error verifying datablock", uuid, mcprofile, err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	postbackurl := ""
	postbackdata := ""
	if passwordok {
		// yay!
		postbackstr := r.Form.Get("data[postback]")
		postback, err := url.Parse(postbackstr)
		if err != nil || (postback.Scheme != "http" && postback.Scheme != "https") {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}

		postbackdata = generateDataBlob(SigningData{
			Now:      time.Now().Unix(),
			UUID:     mcprofile.Id,
			Username: mcprofile.Name,
		}, r.Form.Get("data[siteid]"))
		postbackurl = postback.String()
	}

	je.Encode(struct {
		MCUsername   string `json:"mcusername"`
		UUID         string `json:"uuid"`
		Correct      bool   `json:"correct"`
		Postback     string `json:"postback"`
		PostbackData string `json:"postbackdata"`
	}{
		MCUsername:   mcprofile.Name,
		UUID:         mcprofile.Id,
		Correct:      passwordok,
		Postback:     postbackurl,
		PostbackData: postbackdata,
	})
}

func ApiCreateUserPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		w.Write([]byte("must be a POST request"))
		return
	}

	err := r.ParseForm()
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("data invalid"))
		return
	}

	je := json.NewEncoder(w)

	uuid := r.Form.Get("uuid")
	password := r.Form.Get("password")

	mcprofile, err := minecraft.GetProfile(uuid)
	if err != nil {
		log.Println("error while getting minecraft profile", uuid, err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	skinim, err := minecraft.GetSkin(mcprofile)
	if err != nil {
		log.Println("error while getting skin", uuid, mcprofile, err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	authedim, err := authenticator.Embed(password, skinim)
	if err != nil {
		log.Println("error while embedding into skin", uuid, mcprofile, err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	tmpf, err := ioutil.TempFile("tmpskin/", uuid)
	if err != nil {
		log.Println("error while opening temp file", uuid, mcprofile, err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer tmpf.Close()

	err = png.Encode(tmpf, authedim)
	if err != nil {
		log.Println("error while writing authed skin image", uuid, mcprofile, err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	je.Encode(struct {
		Filename string `json:"filename"`
	}{
		Filename: path.Base(tmpf.Name()),
	})
}

func SkinServerPage(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	f, err := os.Open(fmt.Sprintf("tmpskin/%s", vars["filename"]))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	defer f.Close()

	w.Header().Add("Content-Type", "image/png")
	io.Copy(w, f)
}

func myinit() {
	var flagSesskey string
	var flagAuthenticationKey string
	flag.StringVar(&flagSesskey, "sesskey", "insecure", "session key (used for creating shared secrets with clients)")
	flag.StringVar(&flagAuthenticationKey, "authkey", "insecure", "authentication key (used for hashing passwords)")
	flag.StringVar(&httplistenloc, "listen", ":21333", "HTTP listener location")
	flag.Parse()

	// load the authentication keys
	sesskey = []byte(flagSesskey)
	authenticator = mcassoc.NewAssocifier(flagAuthenticationKey)

	log.Println("Set session key", flagSesskey)
	log.Println("Set authentication key", flagAuthenticationKey)
	log.Println("Going to listen at", httplistenloc)
}

func main() {
	myinit()

	r := mux.NewRouter()
	r.HandleFunc("/", HomePage)
	r.HandleFunc("/perform", PerformPage)
	r.HandleFunc("/api/user/check", ApiCheckUserPage)
	r.HandleFunc("/api/user/create", ApiCreateUserPage)
	r.HandleFunc("/api/user/authenticate", ApiAuthenticateUserPage)
	r.HandleFunc("/media/skin/{filename:[0-9a-z]+}.png", SkinServerPage)
	http.Handle("/", r)

	log.Println("Running!")
	err := http.ListenAndServe(httplistenloc, nil)
	if err != nil {
		log.Fatal("http.ListenAndServe: ", err)
	}
}