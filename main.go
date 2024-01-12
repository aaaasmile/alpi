package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/labstack/gommon/log"

	"alpi/config"
	_ "alpi/plugins/base"
	_ "alpi/plugins/lua"
	_ "alpi/plugins/managesieve"
	_ "alpi/plugins/viewcalendar"
	_ "alpi/plugins/viewhtml"
	_ "alpi/plugins/viewtext"
	"alpi/websrv"
)

func main() {
	var ver = flag.Bool("version", false, "program version")
	var config_file = flag.String("config", "alpi.conf", "configuration filename")
	var theme_path = flag.String("theme", "./themes", "Theme path")

	flag.Parse()
	if *ver {
		fmt.Println(websrv.AppVersion())
		os.Exit(1)
	}
	config, err := config.LoadConfig(*config_file, *theme_path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	e := echo.New()
	e.HideBanner = true
	if l, ok := e.Logger.(*log.Logger); ok {
		l.SetHeader("${time_rfc3339} ${level}")
	}
	if config.Log.File != "" {
		file, err := os.OpenFile(config.Log.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			e.Logger.Errorf("Failed to open log file: %v", err)
		}
		defer file.Close()
		e.Logger.SetOutput(file)
	}

	s, err := websrv.New(e, config)
	if err != nil {
		e.Logger.Fatal(err)
	}
	e.Use(middleware.Recover())
	if config.Log.Debug {
		e.Use(middleware.LoggerWithConfig(middleware.LoggerConfig{
			Format: "${time_rfc3339} method=${method}, uri=${uri}, status=${status}\n",
		}))
		e.Logger.SetLevel(log.DEBUG)
	}

	go e.Start(config.Server.Address)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT)

	for sig := range sigs {
		if sig == syscall.SIGINT {
			break
		}
	}

	ctx, cancel := context.WithDeadline(context.Background(),
		time.Now().Add(30*time.Second))
	e.Shutdown(ctx)
	cancel()

	s.Close()
}
