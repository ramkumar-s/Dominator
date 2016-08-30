package herd

import (
	"fmt"
	"github.com/Symantec/Dominator/lib/constants"
	"github.com/Symantec/Dominator/lib/format"
	"github.com/Symantec/Dominator/lib/srpc"
	"github.com/Symantec/Dominator/lib/url"
	"io"
	"net/http"
	"strings"
	"time"
)

func (herd *Herd) showAliveSubsHandler(w io.Writer, req *http.Request) {
	herd.showSubs(w, "alive ", selectAliveSub)
}

func (herd *Herd) showAllSubsHandler(w io.Writer, req *http.Request) {
	herd.showSubs(w, "", nil)
}

func (herd *Herd) showCompliantSubsHandler(w io.Writer, req *http.Request) {
	herd.showSubs(w, "compliant ", selectCompliantSub)
}

func (herd *Herd) showDeviantSubsHandler(w io.Writer, req *http.Request) {
	herd.showSubs(w, "deviant ", selectDeviantSub)
}

func (herd *Herd) showReachableSubsHandler(w io.Writer, req *http.Request) {
	selector, err := herd.getReachableSelector(url.ParseQuery(req.URL))
	if err != nil {
		fmt.Fprintln(w, err)
		return
	}
	herd.showSubs(w, "reachable ", selector)
}

func (herd *Herd) showSubs(writer io.Writer, subType string,
	selectFunc func(*Sub) bool) {
	fmt.Fprintf(writer, "<title>Dominator %s subs</title>", subType)
	fmt.Fprintln(writer, `<style>
                          table, th, td {
                          border-collapse: collapse;
                          }
                          </style>`)
	if srpc.CheckTlsRequired() {
		fmt.Fprintln(writer, "<body>")
	} else {
		fmt.Fprintln(writer, "<body bgcolor=\"#ffb0b0\">")
		fmt.Fprintln(writer,
			`<h1><center><font color="red">Running in insecure mode. You can get pwned!!!</center></font></h1>`)
	}
	if herd.updatesDisabledReason != "" {
		fmt.Fprintf(writer, "<center>")
		herd.writeDisableStatus(writer)
		fmt.Fprintln(writer, "</center>")
	}
	fmt.Fprintln(writer, `<table border="1" style="width:100%">`)
	fmt.Fprintln(writer, "  <tr>")
	fmt.Fprintln(writer, "    <th>Name</th>")
	fmt.Fprintln(writer, "    <th>Required Image</th>")
	fmt.Fprintln(writer, "    <th>Planned Image</th>")
	fmt.Fprintln(writer, "    <th>Busy</th>")
	fmt.Fprintln(writer, "    <th>Status</th>")
	fmt.Fprintln(writer, "    <th>Uptime</th>")
	fmt.Fprintln(writer, "    <th>Last Scan Duration</th>")
	fmt.Fprintln(writer, "    <th>Staleness</th>")
	fmt.Fprintln(writer, "    <th>Last Update</th>")
	fmt.Fprintln(writer, "    <th>Last Sync</th>")
	fmt.Fprintln(writer, "    <th>Connect</th>")
	fmt.Fprintln(writer, "    <th>Short Poll</th>")
	fmt.Fprintln(writer, "    <th>Full Poll</th>")
	fmt.Fprintln(writer, "    <th>Update Compute</th>")
	fmt.Fprintln(writer, "  </tr>")
	subs := herd.getSelectedSubs(selectFunc)
	for _, sub := range subs {
		showSub(writer, sub)
	}
	fmt.Fprintln(writer, "</table>")
}

func showSub(writer io.Writer, sub *Sub) {
	if sub.isInsecure {
		fmt.Fprintln(writer, "  <tr style=\"background-color:yellow\">")
	} else {
		fmt.Fprintln(writer, "  <tr>")
	}
	subURL := fmt.Sprintf("http://%s:%d/",
		strings.SplitN(sub.String(), "*", 2)[0], constants.SubPortNumber)
	fmt.Fprintf(writer, "    <td><a href=\"%s\">%s</a></td>\n", subURL, sub)
	sub.herd.showImage(writer, sub.mdb.RequiredImage, true)
	sub.herd.showImage(writer, sub.mdb.PlannedImage, false)
	sub.showBusy(writer)
	fmt.Fprintf(writer, "    <td>%s</td>\n", sub.publishedStatus.html())
	timeNow := time.Now()
	showSince(writer, sub.pollTime, sub.startTime)
	showDuration(writer, sub.lastScanDuration, false)
	showSince(writer, timeNow, sub.lastPollSucceededTime)
	showSince(writer, timeNow, sub.lastUpdateTime)
	showSince(writer, timeNow, sub.lastSyncTime)
	showDuration(writer, sub.lastConnectDuration, false)
	showDuration(writer, sub.lastShortPollDuration, !sub.lastPollWasFull)
	showDuration(writer, sub.lastFullPollDuration, sub.lastPollWasFull)
	showDuration(writer, sub.lastComputeUpdateCpuDuration, false)
	fmt.Fprintln(writer, "  </tr>")
}

func (herd *Herd) showImage(writer io.Writer, name string, showDefault bool) {
	if name == "" {
		if showDefault && herd.defaultImageName != "" {
			fmt.Fprintf(writer,
				"    <td><a style=\"color: #CCCC00\" href=\"http://%s/showImage?%s\">%s</a></td>\n",
				herd.imageManager, herd.defaultImageName, herd.defaultImageName)
		} else {
			fmt.Fprintln(writer, "    <td></td>")
		}
	} else if image, err := herd.imageManager.Get(name, false); err != nil {
		fmt.Fprintf(writer, "    <td><font color=\"red\">%s</font></td>\n", err)
	} else if image != nil {
		fmt.Fprintf(writer,
			"    <td><a href=\"http://%s/showImage?%s\">%s</a></td>\n",
			herd.imageManager, name, name)
	} else {
		fmt.Fprintf(writer, "    <td><font color=\"grey\">%s</font></td>\n",
			name)
	}
}

func (sub *Sub) showBusy(writer io.Writer) {
	if sub.busy {
		if sub.busyStartTime.IsZero() {
			fmt.Fprintln(writer, "    <td>busy</td>")
		} else {
			fmt.Fprintf(writer, "    <td>%s</td>\n",
				format.Duration(time.Since(sub.busyStartTime)))
		}
	} else {
		if sub.busyStartTime.IsZero() {
			fmt.Fprintln(writer, "    <td></td>")
		} else {
			fmt.Fprintf(writer, "    <td><font color=\"grey\">%s</font></td>\n",
				format.Duration(sub.busyStopTime.Sub(sub.busyStartTime)))
		}
	}
}

func showSince(writer io.Writer, now time.Time, since time.Time) {
	if now.IsZero() || since.IsZero() {
		fmt.Fprintln(writer, "    <td></td>")
	} else {
		showDuration(writer, now.Sub(since), false)
	}
}

func showDuration(writer io.Writer, duration time.Duration, highlight bool) {
	if duration < 1 {
		fmt.Fprintf(writer, "    <td></td>\n")
	} else {
		str := format.Duration(duration)
		if highlight {
			str = "<b>" + str + "</b>"
		}
		fmt.Fprintf(writer, "    <td>%s</td>\n", str)
	}
}
