// Licensed to Elasticsearch B.V. under one or more contributor
// license agreements. See the NOTICE file distributed with
// this work for additional information regarding copyright
// ownership. Elasticsearch B.V. licenses this file to you under
// the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package snapshot

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/elastic/beats/v7/libbeat/common/transport/httpcommon"
	"github.com/elastic/elastic-agent-poc/internal/pkg/artifact"
	"github.com/elastic/elastic-agent-poc/internal/pkg/artifact/download"
	"github.com/elastic/elastic-agent-poc/internal/pkg/artifact/download/http"
	"github.com/elastic/elastic-agent-poc/internal/pkg/release"
)

// NewDownloader creates a downloader which first checks local directory
// and then fallbacks to remote if configured.
func NewDownloader(config *artifact.Config, versionOverride string) (download.Downloader, error) {
	cfg, err := snapshotConfig(config, versionOverride)
	if err != nil {
		return nil, err
	}
	return http.NewDownloader(cfg)
}

func snapshotConfig(config *artifact.Config, versionOverride string) (*artifact.Config, error) {
	snapshotURI, err := snapshotURI(versionOverride, config)
	if err != nil {
		return nil, fmt.Errorf("failed to detect remote snapshot repo, proceeding with configured: %v", err)
	}

	return &artifact.Config{
		OperatingSystem:       config.OperatingSystem,
		Architecture:          config.Architecture,
		SourceURI:             snapshotURI,
		TargetDirectory:       config.TargetDirectory,
		InstallPath:           config.InstallPath,
		DropPath:              config.DropPath,
		HTTPTransportSettings: config.HTTPTransportSettings,
	}, nil
}

func snapshotURI(versionOverride string, config *artifact.Config) (string, error) {
	version := release.Version()
	if versionOverride != "" {
		if strings.HasSuffix(versionOverride, "-SNAPSHOT") {
			versionOverride = strings.TrimSuffix(versionOverride, "-SNAPSHOT")
		}
		version = versionOverride
	}

	client, err := config.HTTPTransportSettings.Client(httpcommon.WithAPMHTTPInstrumentation())
	if err != nil {
		return "", err
	}

	artifactsURI := fmt.Sprintf("https://artifacts-api.elastic.co/v1/search/%s-SNAPSHOT/elastic-agent", version)
	resp, err := client.Get(artifactsURI)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body := struct {
		Packages map[string]interface{} `json:"packages"`
	}{}

	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&body); err != nil {
		return "", err
	}

	if len(body.Packages) == 0 {
		return "", fmt.Errorf("no packages found in snapshot repo")
	}

	for k, pkg := range body.Packages {
		pkgMap, ok := pkg.(map[string]interface{})
		if !ok {
			return "", fmt.Errorf("content of '%s' is not a map", k)
		}

		uriVal, found := pkgMap["url"]
		if !found {
			return "", fmt.Errorf("item '%s' does not contain url", k)
		}

		uri, ok := uriVal.(string)
		if !ok {
			return "", fmt.Errorf("uri is not a string")
		}

		index := strings.Index(uri, "/beats/elastic-agent/")
		if index == -1 {
			return "", fmt.Errorf("not an agent uri: '%s'", uri)
		}

		return uri[:index], nil
	}

	return "", fmt.Errorf("uri not detected")
}