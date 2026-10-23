// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"gamewe-collector/internal/geoip"
)

// geoipUpdateCmd downloads DB-IP City Lite now (this month's, else last
// month's) into -file, by hand (the collector does it itself with
// geoip.auto_update).
func geoipUpdateCmd(args []string) error {
	fs := flag.NewFlagSet("geoip-update", flag.ExitOnError)
	file := fs.String("file", geoip.DefaultPath, "database file to write (replaced in one step)")
	fs.Parse(args)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	u := &geoip.Updater{Dest: *file}
	now := time.Now().UTC()
	this := now.Format("2006-01")
	last := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0).Format("2006-01")
	err := u.Download(ctx, this)
	if errors.Is(err, geoip.ErrNotPublished) {
		fmt.Fprintf(os.Stderr, "no file for %s yet, taking %s\n", this, last)
		err = u.Download(ctx, last)
	}
	if err != nil {
		return err
	}
	t, _ := geoip.BuildTime(*file)
	fmt.Printf("%s: DB-IP City Lite built %s (CC BY 4.0, IP Geolocation by DB-IP, https://db-ip.com)\n", *file, t.Format("2006-01-02"))
	return nil
}
