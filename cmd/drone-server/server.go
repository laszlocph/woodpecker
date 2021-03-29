// Copyright 2018 Drone.IO Inc.
// Copyright 2021 Informatyka Boguslawski sp. z o.o. sp.k., http://www.ib.pl/
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// This file has been modified by Informatyka Boguslawski sp. z o.o. sp.k.

package main

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"

	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/sync/errgroup"

	"github.com/laszlocph/woodpecker/cncd/logging"
	"github.com/laszlocph/woodpecker/cncd/pipeline/pipeline/rpc/proto"
	"github.com/laszlocph/woodpecker/cncd/pubsub"
	"github.com/laszlocph/woodpecker/plugins/sender"
	"github.com/laszlocph/woodpecker/remote"
	"github.com/laszlocph/woodpecker/router"
	"github.com/laszlocph/woodpecker/router/middleware"
	droneserver "github.com/laszlocph/woodpecker/server"
	"github.com/laszlocph/woodpecker/store"

	"github.com/gin-gonic/contrib/ginrus"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"
	oldcontext "golang.org/x/net/context"
)

var flags = []cli.Flag{
	cli.BoolFlag{
		EnvVar: "DRONE_DEBUG,WOODPECKER_DEBUG",
		Name:   "debug",
		Usage:  "enable server debug mode",
	},
	cli.StringFlag{
		EnvVar: "DRONE_SERVER_HOST,DRONE_HOST,WOODPECKER_SERVER_HOST,WOODPECKER_HOST",
		Name:   "server-host",
		Usage:  "server fully qualified url (<scheme>://<host>)",
	},
	cli.StringFlag{
		EnvVar: "WOODPECKER_HOST_INTERNAL",
		Name:   "server-host-internal",
		Usage:  "server internal fully qualified url (<scheme>://<host>)",
	},
	cli.StringFlag{
		EnvVar: "DRONE_SERVER_ADDR,WOODPECKER_SERVER_ADDR",
		Name:   "server-addr",
		Usage:  "server address",
		Value:  ":8000",
	},
	cli.StringFlag{
		EnvVar: "DRONE_SERVER_CERT,WOODPECKER_SERVER_CERT",
		Name:   "server-cert",
		Usage:  "server ssl cert path",
	},
	cli.StringFlag{
		EnvVar: "DRONE_SERVER_KEY,WOODPECKER_SERVER_KEY",
		Name:   "server-key",
		Usage:  "server ssl key path",
	},
	cli.BoolFlag{
		EnvVar: "DRONE_LETS_ENCRYPT,WOODPECKER_LETS_ENCRYPT",
		Name:   "lets-encrypt",
		Usage:  "enable let's encrypt",
	},
	cli.BoolFlag{
		EnvVar: "DRONE_QUIC,WOODPECKER_QUIC",
		Name:   "quic",
		Usage:  "enable quic",
	},
	cli.StringFlag{
		EnvVar: "DRONE_WWW,WOODPECKER_WWW",
		Name:   "www",
		Usage:  "serve the website from disk",
		Hidden: true,
	},
	cli.StringSliceFlag{
		EnvVar: "DRONE_ADMIN,WOODPECKER_ADMIN",
		Name:   "admin",
		Usage:  "list of admin users",
	},
	cli.StringSliceFlag{
		EnvVar: "DRONE_ORGS,WOODPECKER_ORGS",
		Name:   "orgs",
		Usage:  "list of approved organizations",
	},
	cli.StringSliceFlag{
		EnvVar: "DRONE_REPO_OWNERS,WOODPECKER_REPO_OWNERS",
		Name:   "repo-owners",
		Usage:  "List of syncable repo owners",
	},
	cli.BoolFlag{
		EnvVar: "DRONE_OPEN,WOODPECKER_OPEN",
		Name:   "open",
		Usage:  "enable open user registration",
	},
	cli.StringFlag{
		EnvVar: "DRONE_REPO_CONFIG,WOODPECKER_REPO_CONFIG",
		Name:   "repo-config",
		Usage:  "file path for the drone config",
		Value:  ".drone.yml",
	},
	cli.DurationFlag{
		EnvVar: "DRONE_SESSION_EXPIRES,WOODPECKER_SESSION_EXPIRES",
		Name:   "session-expires",
		Usage:  "session expiration time",
		Value:  time.Hour * 72,
	},
	cli.StringSliceFlag{
		EnvVar: "DRONE_ESCALATE,WOODPECKER_ESCALATE",
		Name:   "escalate",
		Usage:  "images to run in privileged mode",
		Value: &cli.StringSlice{
			"plugins/docker",
			"plugins/gcr",
			"plugins/ecr",
		},
	},
	cli.StringSliceFlag{
		EnvVar: "DRONE_VOLUME,WOODPECKER_VOLUME",
		Name:   "volume",
	},
	cli.StringFlag{
		EnvVar: "DRONE_DOCKER_CONFIG,WOODPECKER_DOCKER_CONFIG",
		Name:   "docker-config",
	},
	cli.StringSliceFlag{
		EnvVar: "DRONE_ENVIRONMENT,WOODPECKER_ENVIRONMENT",
		Name:   "environment",
	},
	cli.StringSliceFlag{
		EnvVar: "DRONE_NETWORK,WOODPECKER_NETWORK",
		Name:   "network",
	},
	cli.StringFlag{
		EnvVar: "DRONE_AGENT_SECRET,DRONE_SECRET,WOODPECKER_AGENT_SECRET,WOODPECKER_SECRET",
		Name:   "agent-secret",
		Usage:  "server-agent shared password",
	},
	cli.StringFlag{
		EnvVar: "DRONE_SECRET_ENDPOINT,WOODPECKER_SECRET_ENDPOINT",
		Name:   "secret-service",
		Usage:  "secret plugin endpoint",
	},
	cli.StringFlag{
		EnvVar: "DRONE_REGISTRY_ENDPOINT,WOODPECKER_REGISTRY_ENDPOINT",
		Name:   "registry-service",
		Usage:  "registry plugin endpoint",
	},
	cli.StringFlag{
		EnvVar: "DRONE_GATEKEEPER_ENDPOINT,WOODPECKER_GATEKEEPER_ENDPOINT",
		Name:   "gating-service",
		Usage:  "gated build endpoint",
	},
	cli.StringFlag{
		EnvVar: "DRONE_DATABASE_DRIVER,DATABASE_DRIVER,WOODPECKER_DATABASE_DRIVER,DATABASE_DRIVER",
		Name:   "driver",
		Usage:  "database driver",
		Value:  "sqlite3",
	},
	cli.StringFlag{
		EnvVar: "DRONE_DATABASE_DATASOURCE,DATABASE_CONFIG,WOODPECKER_DATABASE_DATASOURCE,DATABASE_CONFIG",
		Name:   "datasource",
		Usage:  "database driver configuration string",
		Value:  "drone.sqlite",
	},
	cli.StringFlag{
		EnvVar: "DRONE_PROMETHEUS_AUTH_TOKEN,WOODPECKER_PROMETHEUS_AUTH_TOKEN",
		Name:   "prometheus-auth-token",
		Usage:  "token to secure prometheus metrics endpoint",
		Value:  "",
	},
	//
	// resource limit parameters
	//
	cli.Int64Flag{
		EnvVar: "DRONE_LIMIT_MEM_SWAP,WOODPECKER_LIMIT_MEM_SWAP",
		Name:   "limit-mem-swap",
		Usage:  "maximum swappable memory allowed in bytes",
	},
	cli.Int64Flag{
		EnvVar: "DRONE_LIMIT_MEM,WOODPECKER_LIMIT_MEM",
		Name:   "limit-mem",
		Usage:  "maximum memory allowed in bytes",
	},
	cli.Int64Flag{
		EnvVar: "DRONE_LIMIT_SHM_SIZE,WOODPECKER_LIMIT_SHM_SIZE",
		Name:   "limit-shm-size",
		Usage:  "docker compose /dev/shm allowed in bytes",
	},
	cli.Int64Flag{
		EnvVar: "DRONE_LIMIT_CPU_QUOTA,WOODPECKER_LIMIT_CPU_QUOTA",
		Name:   "limit-cpu-quota",
		Usage:  "impose a cpu quota",
	},
	cli.Int64Flag{
		EnvVar: "DRONE_LIMIT_CPU_SHARES,WOODPECKER_LIMIT_CPU_SHARES",
		Name:   "limit-cpu-shares",
		Usage:  "change the cpu shares",
	},
	cli.StringFlag{
		EnvVar: "DRONE_LIMIT_CPU_SET,WOODPECKER_LIMIT_CPU_SET",
		Name:   "limit-cpu-set",
		Usage:  "set the cpus allowed to execute containers",
	},
	//
	// remote parameters
	//
	cli.BoolFlag{
		EnvVar: "DRONE_GITHUB,WOODPECKER_GITHUB",
		Name:   "github",
		Usage:  "github driver is enabled",
	},
	cli.StringFlag{
		EnvVar: "DRONE_GITHUB_URL,WOODPECKER_GITHUB_URL",
		Name:   "github-server",
		Usage:  "github server address",
		Value:  "https://github.com",
	},
	cli.StringFlag{
		EnvVar: "DRONE_GITHUB_CONTEXT,WOODPECKER_GITHUB_CONTEXT",
		Name:   "github-context",
		Usage:  "github status context",
		Value:  "continuous-integration/drone",
	},
	cli.StringFlag{
		EnvVar: "DRONE_GITHUB_CLIENT,WOODPECKER_GITHUB_CLIENT",
		Name:   "github-client",
		Usage:  "github oauth2 client id",
	},
	cli.StringFlag{
		EnvVar: "DRONE_GITHUB_SECRET,WOODPECKER_GITHUB_SECRET",
		Name:   "github-secret",
		Usage:  "github oauth2 client secret",
	},
	cli.StringSliceFlag{
		EnvVar: "DRONE_GITHUB_SCOPE,WOODPECKER_GITHUB_SCOPE",
		Name:   "github-scope",
		Usage:  "github oauth scope",
		Value: &cli.StringSlice{
			"repo",
			"repo:status",
			"user:email",
			"read:org",
		},
	},
	cli.StringFlag{
		EnvVar: "DRONE_GITHUB_GIT_USERNAME,WOODPECKER_GITHUB_GIT_USERNAME",
		Name:   "github-git-username",
		Usage:  "github machine user username",
	},
	cli.StringFlag{
		EnvVar: "DRONE_GITHUB_GIT_PASSWORD,WOODPECKER_GITHUB_GIT_PASSWORD",
		Name:   "github-git-password",
		Usage:  "github machine user password",
	},
	cli.BoolTFlag{
		EnvVar: "DRONE_GITHUB_MERGE_REF,WOODPECKER_GITHUB_MERGE_REF",
		Name:   "github-merge-ref",
		Usage:  "github pull requests use merge ref",
	},
	cli.BoolFlag{
		EnvVar: "DRONE_GITHUB_PRIVATE_MODE,WOODPECKER_GITHUB_PRIVATE_MODE",
		Name:   "github-private-mode",
		Usage:  "github is running in private mode",
	},
	cli.BoolFlag{
		EnvVar: "DRONE_GITHUB_SKIP_VERIFY,WOODPECKER_GITHUB_SKIP_VERIFY",
		Name:   "github-skip-verify",
		Usage:  "github skip ssl verification",
	},
	cli.BoolFlag{
		EnvVar: "DRONE_GOGS,WOODPECKER_GOGS",
		Name:   "gogs",
		Usage:  "gogs driver is enabled",
	},
	cli.StringFlag{
		EnvVar: "DRONE_GOGS_URL,WOODPECKER_GOGS_URL",
		Name:   "gogs-server",
		Usage:  "gogs server address",
		Value:  "https://github.com",
	},
	cli.StringFlag{
		EnvVar: "DRONE_GOGS_GIT_USERNAME,WOODPECKER_GOGS_GIT_USERNAME",
		Name:   "gogs-git-username",
		Usage:  "gogs service account username",
	},
	cli.StringFlag{
		EnvVar: "DRONE_GOGS_GIT_PASSWORD,WOODPECKER_GOGS_GIT_PASSWORD",
		Name:   "gogs-git-password",
		Usage:  "gogs service account password",
	},
	cli.BoolFlag{
		EnvVar: "DRONE_GOGS_PRIVATE_MODE,WOODPECKER_GOGS_PRIVATE_MODE",
		Name:   "gogs-private-mode",
		Usage:  "gogs private mode enabled",
	},
	cli.BoolFlag{
		EnvVar: "DRONE_GOGS_SKIP_VERIFY,WOODPECKER_GOGS_SKIP_VERIFY",
		Name:   "gogs-skip-verify",
		Usage:  "gogs skip ssl verification",
	},
	cli.BoolFlag{
		EnvVar: "DRONE_GITEA,WOODPECKER_GITEA",
		Name:   "gitea",
		Usage:  "gitea driver is enabled",
	},
	cli.StringFlag{
		EnvVar: "DRONE_GITEA_URL,WOODPECKER_GITEA_URL",
		Name:   "gitea-server",
		Usage:  "gitea server address",
		Value:  "https://try.gitea.io",
	},
	cli.StringFlag{
		EnvVar: "DRONE_GITEA_CONTEXT,WOODPECKER_GITEA_CONTEXT",
		Name:   "gitea-context",
		Usage:  "gitea status context",
		Value:  "continuous-integration/drone",
	},
	cli.StringFlag{
		EnvVar: "DRONE_GITEA_GIT_USERNAME,WOODPECKER_GITEA_GIT_USERNAME",
		Name:   "gitea-git-username",
		Usage:  "gitea service account username",
	},
	cli.StringFlag{
		EnvVar: "DRONE_GITEA_GIT_PASSWORD,WOODPECKER_GITEA_GIT_PASSWORD",
		Name:   "gitea-git-password",
		Usage:  "gitea service account password",
	},
	cli.BoolFlag{
		EnvVar: "DRONE_GITEA_PRIVATE_MODE,WOODPECKER_GITEA_PRIVATE_MODE",
		Name:   "gitea-private-mode",
		Usage:  "gitea private mode enabled",
	},
	cli.BoolFlag{
		EnvVar: "DRONE_GITEA_SKIP_VERIFY,WOODPECKER_GITEA_SKIP_VERIFY",
		Name:   "gitea-skip-verify",
		Usage:  "gitea skip ssl verification",
	},
	cli.BoolFlag{
		EnvVar: "DRONE_BITBUCKET,WOODPECKER_BITBUCKET",
		Name:   "bitbucket",
		Usage:  "bitbucket driver is enabled",
	},
	cli.StringFlag{
		EnvVar: "DRONE_BITBUCKET_CLIENT,WOODPECKER_BITBUCKET_CLIENT",
		Name:   "bitbucket-client",
		Usage:  "bitbucket oauth2 client id",
	},
	cli.StringFlag{
		EnvVar: "DRONE_BITBUCKET_SECRET,WOODPECKER_BITBUCKET_SECRET",
		Name:   "bitbucket-secret",
		Usage:  "bitbucket oauth2 client secret",
	},
	cli.BoolFlag{
		EnvVar: "DRONE_GITLAB,WOODPECKER_GITLAB",
		Name:   "gitlab",
		Usage:  "gitlab driver is enabled",
	},
	cli.StringFlag{
		EnvVar: "DRONE_GITLAB_URL,WOODPECKER_GITLAB_URL",
		Name:   "gitlab-server",
		Usage:  "gitlab server address",
		Value:  "https://gitlab.com",
	},
	cli.StringFlag{
		EnvVar: "DRONE_GITLAB_CLIENT,WOODPECKER_GITLAB_CLIENT",
		Name:   "gitlab-client",
		Usage:  "gitlab oauth2 client id",
	},
	cli.StringFlag{
		EnvVar: "DRONE_GITLAB_SECRET,WOODPECKER_GITLAB_SECRET",
		Name:   "gitlab-secret",
		Usage:  "gitlab oauth2 client secret",
	},
	cli.StringFlag{
		EnvVar: "DRONE_GITLAB_GIT_USERNAME,WOODPECKER_GITLAB_GIT_USERNAME",
		Name:   "gitlab-git-username",
		Usage:  "gitlab service account username",
	},
	cli.StringFlag{
		EnvVar: "DRONE_GITLAB_GIT_PASSWORD,WOODPECKER_GITLAB_GIT_PASSWORD",
		Name:   "gitlab-git-password",
		Usage:  "gitlab service account password",
	},
	cli.BoolFlag{
		EnvVar: "DRONE_GITLAB_SKIP_VERIFY,WOODPECKER_GITLAB_SKIP_VERIFY",
		Name:   "gitlab-skip-verify",
		Usage:  "gitlab skip ssl verification",
	},
	cli.BoolFlag{
		EnvVar: "DRONE_GITLAB_PRIVATE_MODE,WOODPECKER_GITLAB_PRIVATE_MODE",
		Name:   "gitlab-private-mode",
		Usage:  "gitlab is running in private mode",
	},
	cli.BoolFlag{
		EnvVar: "DRONE_GITLAB_V3_API,WOODPECKER_GITLAB_V3_API",
		Name:   "gitlab-v3-api",
		Usage:  "gitlab is running the v3 api",
	},
	cli.BoolFlag{
		EnvVar: "DRONE_STASH,WOODPECKER_STASH",
		Name:   "stash",
		Usage:  "stash driver is enabled",
	},
	cli.StringFlag{
		EnvVar: "DRONE_STASH_URL,WOODPECKER_STASH_URL",
		Name:   "stash-server",
		Usage:  "stash server address",
	},
	cli.StringFlag{
		EnvVar: "DRONE_STASH_CONSUMER_KEY,WOODPECKER_STASH_CONSUMER_KEY",
		Name:   "stash-consumer-key",
		Usage:  "stash oauth1 consumer key",
	},
	cli.StringFlag{
		EnvVar: "DRONE_STASH_CONSUMER_RSA,WOODPECKER_STASH_CONSUMER_RSA",
		Name:   "stash-consumer-rsa",
		Usage:  "stash oauth1 private key file",
	},
	cli.StringFlag{
		EnvVar: "DRONE_STASH_CONSUMER_RSA_STRING,WOODPECKER_STASH_CONSUMER_RSA_STRING",
		Name:   "stash-consumer-rsa-string",
		Usage:  "stash oauth1 private key string",
	},
	cli.StringFlag{
		EnvVar: "DRONE_STASH_GIT_USERNAME,WOODPECKER_STASH_GIT_USERNAME",
		Name:   "stash-git-username",
		Usage:  "stash service account username",
	},
	cli.StringFlag{
		EnvVar: "DRONE_STASH_GIT_PASSWORD,WOODPECKER_STASH_GIT_PASSWORD",
		Name:   "stash-git-password",
		Usage:  "stash service account password",
	},
	cli.BoolFlag{
		EnvVar: "DRONE_STASH_SKIP_VERIFY,WOODPECKER_STASH_SKIP_VERIFY",
		Name:   "stash-skip-verify",
		Usage:  "stash skip ssl verification",
	},
	cli.BoolFlag{
		EnvVar: "DRONE_CODING,WOODPECKER_CODING",
		Name:   "coding",
		Usage:  "coding driver is enabled",
	},
	cli.StringFlag{
		EnvVar: "DRONE_CODING_URL,WOODPECKER_CODING_URL",
		Name:   "coding-server",
		Usage:  "coding server address",
		Value:  "https://coding.net",
	},
	cli.StringFlag{
		EnvVar: "DRONE_CODING_CLIENT,WOODPECKER_CODING_CLIENT",
		Name:   "coding-client",
		Usage:  "coding oauth2 client id",
	},
	cli.StringFlag{
		EnvVar: "DRONE_CODING_SECRET,WOODPECKER_CODING_SECRET",
		Name:   "coding-secret",
		Usage:  "coding oauth2 client secret",
	},
	cli.StringSliceFlag{
		EnvVar: "DRONE_CODING_SCOPE,WOODPECKER_CODING_SCOPE",
		Name:   "coding-scope",
		Usage:  "coding oauth scope",
		Value: &cli.StringSlice{
			"user",
			"project",
			"project:depot",
		},
	},
	cli.StringFlag{
		EnvVar: "DRONE_CODING_GIT_MACHINE,WOODPECKER_CODING_GIT_MACHINE",
		Name:   "coding-git-machine",
		Usage:  "coding machine name",
		Value:  "git.coding.net",
	},
	cli.StringFlag{
		EnvVar: "DRONE_CODING_GIT_USERNAME,WOODPECKER_CODING_GIT_USERNAME",
		Name:   "coding-git-username",
		Usage:  "coding machine user username",
	},
	cli.StringFlag{
		EnvVar: "DRONE_CODING_GIT_PASSWORD,WOODPECKER_CODING_GIT_PASSWORD",
		Name:   "coding-git-password",
		Usage:  "coding machine user password",
	},
	cli.BoolFlag{
		EnvVar: "DRONE_CODING_SKIP_VERIFY,WOODPECKER_CODING_SKIP_VERIFY",
		Name:   "coding-skip-verify",
		Usage:  "coding skip ssl verification",
	},
	cli.DurationFlag{
		EnvVar: "DRONE_KEEPALIVE_MIN_TIME,WOODPECKER_KEEPALIVE_MIN_TIME",
		Name:   "keepalive-min-time",
		Usage:  "server-side enforcement policy on the minimum amount of time a client should wait before sending a keepalive ping.",
	},
	cli.BoolFlag{
		EnvVar: "WOODPECKER_GITEA_REV_PROXY_AUTH",
		Name:   "gitea-rev-proxy-auth",
		Usage:  "enable gitea authentication using HTTP header specified in WOODPECKER_GITEA_REV_PROXY_AUTH_HEADER",
	},
	cli.StringFlag{
		EnvVar: "WOODPECKER_GITEA_REV_PROXY_AUTH_HEADER",
		Name:   "gitea-rev-proxy-auth-header",
		Usage:  "HTTP header with gitea authenticated user login",
	},
}

func server(c *cli.Context) error {

	// debug level if requested by user
	if c.Bool("debug") {
		logrus.SetLevel(logrus.DebugLevel)
	} else {
		logrus.SetLevel(logrus.WarnLevel)
	}

	// must configure the drone_host variable
	if c.String("server-host") == "" {
		logrus.Fatalln("DRONE_HOST/DRONE_SERVER_HOST is not properly configured")
	}

	if !strings.Contains(c.String("server-host"), "://") {
		logrus.Fatalln(
			"DRONE_HOST/DRONE_SERVER_HOST must be <scheme>://<hostname> format",
		)
	}

	if strings.HasSuffix(c.String("server-host"), "/") {
		logrus.Fatalln(
			"DRONE_HOST/DRONE_SERVER_HOST must not have trailing slash",
		)
	}

	if c.String("server-host-internal") != "" && !strings.Contains(c.String("server-host-internal"), "://") {
		logrus.Fatalln(
			"WOODPECKER_HOST_INTERNAL must be <scheme>://<hostname> format",
		)
	}

	if c.String("server-host-internal") != "" && strings.HasSuffix(c.String("server-host-internal"), "/") {
		logrus.Fatalln(
			"WOODPECKER_HOST_INTERNAL must not have trailing slash",
		)
	}

	remote_, err := SetupRemote(c)
	if err != nil {
		logrus.Fatal(err)
	}

	store_ := setupStore(c)
	setupEvilGlobals(c, store_, remote_)

	// we are switching from gin to httpservermux|treemux,
	// so if this code looks strange, that is why.
	tree := setupTree(c)

	// setup the server and start the listener
	handler := router.Load(
		tree,
		ginrus.Ginrus(logrus.StandardLogger(), time.RFC3339, true),
		middleware.Version,
		middleware.Config(c),
		middleware.Store(c, store_),
		middleware.Remote(remote_),
	)

	var g errgroup.Group

	// start the grpc server
	g.Go(func() error {

		lis, err := net.Listen("tcp", ":9000")
		if err != nil {
			logrus.Error(err)
			return err
		}
		auther := &authorizer{
			password: c.String("agent-secret"),
		}
		grpcServer := grpc.NewServer(
			grpc.StreamInterceptor(auther.streamInterceptor),
			grpc.UnaryInterceptor(auther.unaryIntercaptor),
			grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
				MinTime: c.Duration("keepalive-min-time"),
			}),
		)
		droneServer := droneserver.NewDroneServer(remote_, droneserver.Config.Services.Queue, droneserver.Config.Services.Logs, droneserver.Config.Services.Pubsub, store_, droneserver.Config.Server.Host)
		proto.RegisterDroneServer(grpcServer, droneServer)

		err = grpcServer.Serve(lis)
		if err != nil {
			logrus.Error(err)
			return err
		}
		return nil
	})

	setupMetrics(&g, store_)

	// start the server with tls enabled
	if c.String("server-cert") != "" {
		g.Go(func() error {
			return http.ListenAndServe(":http", http.HandlerFunc(redirect))
		})
		g.Go(func() error {
			serve := &http.Server{
				Addr:    ":https",
				Handler: handler,
				TLSConfig: &tls.Config{
					NextProtos: []string{"http/1.1"}, // disable h2 because Safari :(
				},
			}
			return serve.ListenAndServeTLS(
				c.String("server-cert"),
				c.String("server-key"),
			)
		})
		return g.Wait()
	}

	// start the server without tls enabled
	if !c.Bool("lets-encrypt") {
		return http.ListenAndServe(
			c.String("server-addr"),
			handler,
		)
	}

	// start the server with lets encrypt enabled
	// listen on ports 443 and 80
	address, err := url.Parse(c.String("server-host"))
	if err != nil {
		return err
	}

	dir := cacheDir()
	os.MkdirAll(dir, 0700)

	manager := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(address.Host),
		Cache:      autocert.DirCache(dir),
	}
	g.Go(func() error {
		return http.ListenAndServe(":http", manager.HTTPHandler(http.HandlerFunc(redirect)))
	})
	g.Go(func() error {
		serve := &http.Server{
			Addr:    ":https",
			Handler: handler,
			TLSConfig: &tls.Config{
				GetCertificate: manager.GetCertificate,
				NextProtos:     []string{"http/1.1"}, // disable h2 because Safari :(
			},
		}
		return serve.ListenAndServeTLS("", "")
	})

	return g.Wait()
}

func setupEvilGlobals(c *cli.Context, v store.Store, r remote.Remote) {

	// storage
	droneserver.Config.Storage.Files = v
	droneserver.Config.Storage.Config = v

	// services
	droneserver.Config.Services.Queue = setupQueue(c, v)
	droneserver.Config.Services.Logs = logging.New()
	droneserver.Config.Services.Pubsub = pubsub.New()
	droneserver.Config.Services.Pubsub.Create(context.Background(), "topic/events")
	droneserver.Config.Services.Registries = setupRegistryService(c, v)
	droneserver.Config.Services.Secrets = setupSecretService(c, v)
	droneserver.Config.Services.Senders = sender.New(v, v)
	droneserver.Config.Services.Environ = setupEnvironService(c, v)

	if endpoint := c.String("gating-service"); endpoint != "" {
		droneserver.Config.Services.Senders = sender.NewRemote(endpoint)
	}

	// limits
	droneserver.Config.Pipeline.Limits.MemSwapLimit = c.Int64("limit-mem-swap")
	droneserver.Config.Pipeline.Limits.MemLimit = c.Int64("limit-mem")
	droneserver.Config.Pipeline.Limits.ShmSize = c.Int64("limit-shm-size")
	droneserver.Config.Pipeline.Limits.CPUQuota = c.Int64("limit-cpu-quota")
	droneserver.Config.Pipeline.Limits.CPUShares = c.Int64("limit-cpu-shares")
	droneserver.Config.Pipeline.Limits.CPUSet = c.String("limit-cpu-set")

	// server configuration
	droneserver.Config.Server.Cert = c.String("server-cert")
	droneserver.Config.Server.Key = c.String("server-key")
	droneserver.Config.Server.Pass = c.String("agent-secret")
	droneserver.Config.Server.Host = strings.TrimRight(c.String("server-host"), "/")
	droneserver.Config.Server.HostInternal = strings.TrimRight(c.String("server-host-internal"), "/")
	droneserver.Config.Server.Port = c.String("server-addr")
	droneserver.Config.Server.RepoConfig = c.String("repo-config")
	droneserver.Config.Server.SessionExpires = c.Duration("session-expires")
	droneserver.Config.Pipeline.Networks = c.StringSlice("network")
	droneserver.Config.Pipeline.Volumes = c.StringSlice("volume")
	droneserver.Config.Pipeline.Privileged = c.StringSlice("escalate")
	// droneserver.Config.Server.Open = cli.Bool("open")
	// droneserver.Config.Server.Orgs = sliceToMap(cli.StringSlice("orgs"))
	// droneserver.Config.Server.Admins = sliceToMap(cli.StringSlice("admin"))

	// prometheus
	droneserver.Config.Prometheus.AuthToken = c.String("prometheus-auth-token")
}

type authorizer struct {
	username string
	password string
}

func (a *authorizer) streamInterceptor(srv interface{}, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if err := a.authorize(stream.Context()); err != nil {
		return err
	}
	return handler(srv, stream)
}

func (a *authorizer) unaryIntercaptor(ctx oldcontext.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
	if err := a.authorize(ctx); err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

func (a *authorizer) authorize(ctx context.Context) error {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if len(md["password"]) > 0 && md["password"][0] == a.password {
			return nil
		}
		return errors.New("invalid agent token")
	}
	return errors.New("missing agent token")
}

func redirect(w http.ResponseWriter, req *http.Request) {
	var serverHost string = droneserver.Config.Server.Host
	serverHost = strings.TrimPrefix(serverHost, "http://")
	serverHost = strings.TrimPrefix(serverHost, "https://")
	req.URL.Scheme = "https"
	req.URL.Host = serverHost

	w.Header().Set("Strict-Transport-Security", "max-age=31536000")

	http.Redirect(w, req, req.URL.String(), http.StatusMovedPermanently)
}

func cacheDir() string {
	const base = "golang-autocert"
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		return filepath.Join(xdg, base)
	}
	return filepath.Join(os.Getenv("HOME"), ".cache", base)
}
