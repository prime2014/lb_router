package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"router"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

var (
	configFile string
	port       string

	rootCmd = &cobra.Command{
		Use:   "xpress",
		Short: "Basic essentials on how to start the load balancer",
		Long: `This documentation provides essential details on how to interact
		      with the load-balancer`,
	}

	startCmd = &cobra.Command{
		Use:   "start",
		Short: "Run start to wake the loadbalancer",
		Run: func(cmd *cobra.Command, args []string) {
			cfg, err := loadConfig(configFile)

			if err != nil {
				log.Fatal().Err(err).Msg("failed to load configuration")
			}

			log.Info().
				Str("mode", string(cfg.Router.Mode)).
				Str("algorithm", string(cfg.Router.Algorithm)).
				Strs("backend", cfg.Router.URLs).
				Msg("Starting Xpress load balancer")

			ctx := context.Background()

			routerHandler := router.NewRouterHandler(cfg.Router, ctx)

			routerHandler.StartServer(fmt.Sprintf("0.0.0.0:%s", port), "", "")

		},
	}
)

func loadConfig(path string) (*router.Config, error) {
	v := viper.New()

	if path != "" {
		v.SetConfigFile(path)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath("/etc/xpress/")
	}

	// Enable reading environment variables (e.g XPRESS_ROUTER_MODE=tcp)
	v.SetEnvPrefix("XPRESS")
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		return nil, err
	}

	var cfg router.Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func init() {
	// register flags for the start command
	startCmd.Flags().StringVarP(&configFile, "config", "c", "", "Path to configuration file (default: ./config.yaml)")
	startCmd.Flags().StringVarP(&port, "port", "p", "8080", "the listener port for the loadbalancer")

	// Attach subcommands to root
	rootCmd.AddCommand(startCmd)
}

func initLogger() {
	logDir := "./logs"

	activeLogFile := filepath.Join(logDir, "xpress.log")
	symlinkFile := filepath.Join(logDir, "current.log")

	// 1. File Writer with Rotation (10 MB size, retain 5 backups, 30 days max age)
	fileWriter := router.NewRotatingFileWriter(activeLogFile, symlinkFile, 10, 5, 30)

	// Stdout console writer (Pretty human-readable output)
	consoleWriter := zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: "2006-01-02 15:04:05",
	}

	multiWriter := io.MultiWriter(consoleWriter, fileWriter)

	zerolog.SetGlobalLevel(zerolog.DebugLevel)
	zerolog.New(multiWriter).With().Timestamp().Caller().Logger()
}

func main() {
	initLogger()

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error executing xpress CLI: %v\n", err)
		os.Exit(1)
	}
}
