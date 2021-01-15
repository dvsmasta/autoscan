package plex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"

	"github.com/cloudbox/autoscan"
	"github.com/rs/zerolog"
)

type Config struct {
	URL       string             `yaml:"url"`
	Token     string             `yaml:"token"`
	Rewrite   []autoscan.Rewrite `yaml:"rewrite"`
	Verbosity string             `yaml:"verbosity"`
}

type target struct {
	url       string
	token     string
	libraries []library

	log     zerolog.Logger
	rewrite autoscan.Rewriter
	api     *apiClient
}

func New(c Config) (autoscan.Target, error) {
	l := autoscan.GetLogger(c.Verbosity).With().
		Str("target", "plex").
		Str("url", c.URL).Logger()

	rewriter, err := autoscan.NewRewriter(c.Rewrite)
	if err != nil {
		return nil, err
	}

	api := newAPIClient(c.URL, c.Token, l)

	version, err := api.Version()
	if err != nil {
		return nil, err
	}

	l.Debug().Msgf("Plex version: %s", version)
	if !isSupportedVersion(version) {
		return nil, fmt.Errorf("plex running unsupported version %s: %w", version, autoscan.ErrFatal)
	}

	libraries, err := api.Libraries()
	if err != nil {
		return nil, err
	}

	l.Debug().
		Interface("libraries", libraries).
		Msg("Retrieved libraries")

	return &target{
		url:       c.URL,
		token:     c.Token,
		libraries: libraries,

		log:     l,
		rewrite: rewriter,
		api:     api,
	}, nil
}

func (t target) Available() error {
	_, err := t.api.Version()
	return err
}

type rclonerc map[string]interface{}

func rcrefresh(Data *rclonerc, url string) string {

	jsonData, _ := json.Marshal(Data)
	fmt.Println("Json String", string(jsonData))
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	fmt.Println("response Status:", resp.Status)
	fmt.Println("response Headers:", resp.Header)
	body, _ := ioutil.ReadAll(resp.Body)
	bodystring := string(body)
	fmt.Println("response Body:", bodystring)
	return bodystring
}

func (t target) Scan(scan autoscan.Scan) error {
	// determine library for this scan
	scanFolder := t.rewrite(scan.Folder)

	libs, err := t.getScanLibrary(scanFolder)
	if err != nil {
		t.log.Warn().
			Err(err).
			Msg("No target libraries found")

		return nil
	}

	url := "http://192.168.1.172:5570/vfs%2Frefresh"
	url2 := "http://192.168.1.15:5572/vfs%2Frefresh"

	s := strings.TrimPrefix(scanFolder, "/mnt/unionfs/Media")
	fmt.Println("Trimmed String:", s)

	base_dir := s[strings.LastIndex(s, "/"):]
	base_dir = strings.TrimSuffix(s, base_dir)
	fmt.Println("Base Dir Trim:", base_dir)

	firstrequest := rclonerc{
		"recursive": "true",
		"dir":       s,
	}
	resp := rcrefresh(&firstrequest, url)
	rcrefresh(&firstrequest, url2)
	if strings.Contains(resp, "file does not exist") {
		secondrequest := rclonerc{
			"recursive": "false",
			"dir":       base_dir,
		}

		resp2 := rcrefresh(&secondrequest, url)
		rcrefresh(&secondrequest, url2)
		if strings.Contains(resp2, "OK") {
			fmt.Println("Third request var s:", s)

			thirdrequest := rclonerc{
				"recursive": "true",
				"dir":       s,
			}
			rcrefresh(&thirdrequest, url)
			rcrefresh(&thirdrequest, url2)

		} else {

			// this means its a new tv show possibly and the main directory doesnt exist
			// so lets go down 1 more directory and do a recurse false to make it pop

			base_dirtmp := base_dir[strings.LastIndex(base_dir, "/"):]
			new_base_dir := strings.TrimSuffix(base_dir, base_dirtmp)
			fmt.Println("Fourth request Base Dir Trim:", new_base_dir)

			fourthrequest := rclonerc{
				"recursive": "false",
				"dir":       new_base_dir,
			}

			rcrefresh(&fourthrequest, url)
			rcrefresh(&fourthrequest, url2)

		}

	}

	// send scan request
	for _, lib := range libs {
		l := t.log.With().
			Str("path", scanFolder).
			Str("library", lib.Name).
			Logger()

		l.Trace().Msg("Sending scan request")

		if err := t.api.Scan(scanFolder, lib.ID); err != nil {
			return err
		}

		l.Info().Msg("Scan moved to target")
	}

	return nil
}

func (t target) getScanLibrary(folder string) ([]library, error) {
	libraries := make([]library, 0)

	for _, l := range t.libraries {
		if strings.HasPrefix(folder, l.Path) {
			libraries = append(libraries, l)
		}
	}

	if len(libraries) == 0 {
		return nil, fmt.Errorf("%v: failed determining libraries", folder)
	}

	return libraries, nil
}

func isSupportedVersion(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return false
	}

	major, _ := strconv.Atoi(parts[0])
	minor, _ := strconv.Atoi(parts[1])

	if major >= 2 || (major == 1 && minor >= 20) {
		return true
	}

	return false
}
