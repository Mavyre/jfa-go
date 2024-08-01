package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	lm "github.com/hrfee/jfa-go/logmessages"
)

// GenInternalReset generates a local password reset PIN, for use with the PWR option on the Admin page.
func (app *appContext) GenInternalReset(userID string) (InternalPWR, error) {
	pin := genAuthToken()
	user, status, err := app.jf.UserByID(userID, false)
	if err != nil || status != 200 {
		return InternalPWR{}, err
	}
	pwr := InternalPWR{
		PIN:      pin,
		Username: user.Name,
		ID:       userID,
		Expiry:   time.Now().Add(30 * time.Minute),
	}
	return pwr, nil
}

// GenResetLink generates and returns a password reset link.
func (app *appContext) GenResetLink(pin string) (string, error) {
	url := app.config.Section("password_resets").Key("url_base").String()
	var pinLink string
	if url == "" {
		return pinLink, fmt.Errorf("disabled as no URL Base provided. Set in Settings > Password Resets.")
	}
	// Strip /invite from end of this URL, ik it's ugly.
	pinLink = fmt.Sprintf("%s/reset?pin=%s", url, pin)
	return pinLink, nil
}

func (app *appContext) StartPWR() {
	app.info.Println(lm.StartDaemon, "PWR")
	path := app.config.Section("password_resets").Key("watch_directory").String()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		app.err.Printf(lm.FailedStartDaemon, "PWR", fmt.Sprintf(lm.PathNotFound, path))
		return
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		app.err.Printf(lm.FailedStartDaemon, "PWR", err)
		return
	}
	defer watcher.Close()

	go pwrMonitor(app, watcher)
	err = watcher.Add(path)
	if err != nil {
		app.err.Printf(lm.FailedStartDaemon, "PWR", err)
	}

	waitForRestart()
}

// PasswordReset represents a passwordreset-xyz.json file generated by Jellyfin.
type PasswordReset struct {
	Pin      string    `json:"Pin"`
	Username string    `json:"UserName"`
	Expiry   time.Time `json:"ExpirationDate"`
	Internal bool      `json:"Internal,omitempty"`
}

func pwrMonitor(app *appContext, watcher *fsnotify.Watcher) {
	if !emailEnabled {
		return
	}
	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&fsnotify.Write == fsnotify.Write && strings.Contains(event.Name, "passwordreset") {
				var pwr PasswordReset
				data, err := os.ReadFile(event.Name)
				if err != nil {
					app.debug.Printf(lm.FailedReading, event.Name, err)
					return
				}
				err = json.Unmarshal(data, &pwr)
				if len(pwr.Pin) == 0 || err != nil {
					app.debug.Printf(lm.FailedReading, event.Name, err)
					continue
				}
				app.info.Printf("New password reset for user \"%s\"", pwr.Username)
				if currentTime := time.Now(); pwr.Expiry.After(currentTime) {
					user, status, err := app.jf.UserByName(pwr.Username, false)
					if !(status == 200 || status == 204) || err != nil || user.ID == "" {
						app.err.Printf(lm.FailedGetUser, pwr.Username, lm.Jellyfin, err)
						return
					}
					uid := user.ID
					name := app.getAddressOrName(uid)
					if name != "" {
						msg, err := app.email.constructReset(pwr, app, false)

						if err != nil {
							app.err.Printf(lm.FailedConstructPWRMessage, pwr.Username, err)
						} else if err := app.sendByID(msg, uid); err != nil {
							app.err.Printf(lm.FailedSendPWRMessage, pwr.Username, name, err)
						} else {
							app.err.Printf(lm.SentPWRMessage, pwr.Username, name)
						}
					}
				} else {
					app.err.Printf(lm.PWRExpired, pwr.Username, pwr.Expiry)
				}

			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			app.err.Printf(lm.FailedStartDaemon, "PWR", err)
		}
	}
}
