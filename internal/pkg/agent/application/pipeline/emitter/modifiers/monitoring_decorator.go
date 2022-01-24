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

package modifiers

import (
	"crypto/md5"
	"fmt"

	"github.com/elastic/elastic-agent-poc/internal/pkg/agent/application/info"
	"github.com/elastic/elastic-agent-poc/internal/pkg/agent/program"
	"github.com/elastic/elastic-agent-poc/internal/pkg/agent/transpiler"
)

const (
	// MonitoringName is a name used for artificial program generated when monitoring is needed.
	MonitoringName            = "FLEET_MONITORING"
	programsKey               = "programs"
	monitoringChecksumKey     = "monitoring_checksum"
	monitoringKey             = "agent.monitoring"
	monitoringUseOutputKey    = "agent.monitoring.use_output"
	monitoringOutputFormatKey = "outputs.%s"
	outputKey                 = "output"

	enabledKey        = "agent.monitoring.enabled"
	logsKey           = "agent.monitoring.logs"
	metricsKey        = "agent.monitoring.metrics"
	outputsKey        = "outputs"
	elasticsearchKey  = "elasticsearch"
	typeKey           = "type"
	defaultOutputName = "default"
)

// InjectMonitoring injects a monitoring configuration into a group of programs if needed.
func InjectMonitoring(agentInfo *info.AgentInfo, outputGroup string, rootAst *transpiler.AST, programsToRun []program.Program) ([]program.Program, error) {
	var err error
	monitoringProgram := program.Program{
		Spec: program.Spec{
			Name: MonitoringName,
			Cmd:  MonitoringName,
		},
	}

	// if monitoring is not specified use default one where everything is enabled
	if _, found := transpiler.Lookup(rootAst, monitoringKey); !found {
		monitoringNode := transpiler.NewDict([]transpiler.Node{
			transpiler.NewKey("enabled", transpiler.NewBoolVal(true)),
			transpiler.NewKey("logs", transpiler.NewBoolVal(true)),
			transpiler.NewKey("metrics", transpiler.NewBoolVal(true)),
			transpiler.NewKey("use_output", transpiler.NewStrVal("default")),
			transpiler.NewKey("namespace", transpiler.NewStrVal("default")),
		})

		transpiler.Insert(rootAst, transpiler.NewKey("monitoring", monitoringNode), "settings")
	}

	// get monitoring output name to be used
	monitoringOutputName, found := transpiler.LookupString(rootAst, monitoringUseOutputKey)
	if !found {
		monitoringOutputName = defaultOutputName
	}

	typeValue, found := transpiler.LookupString(rootAst, fmt.Sprintf("%s.%s.type", outputsKey, monitoringOutputName))
	if !found {
		typeValue = elasticsearchKey
	}

	ast := rootAst.Clone()
	if err := getMonitoringRule(monitoringOutputName, typeValue).Apply(agentInfo, ast); err != nil {
		return programsToRun, err
	}

	config, err := ast.Map()
	if err != nil {
		return programsToRun, err
	}

	programList := make([]string, 0, len(programsToRun))
	cfgHash := md5.New()
	for _, p := range programsToRun {
		programList = append(programList, p.Spec.Cmd)
		cfgHash.Write(p.Config.Hash())
	}
	// making program list and their hashes part of the config
	// so it will get regenerated with every change
	config[programsKey] = programList
	config[monitoringChecksumKey] = fmt.Sprintf("%x", cfgHash.Sum(nil))

	monitoringProgram.Config, err = transpiler.NewAST(config)
	if err != nil {
		return programsToRun, err
	}

	return append(programsToRun, monitoringProgram), nil
}

func getMonitoringRule(outputName string, t string) *transpiler.RuleList {
	monitoringOutputSelector := fmt.Sprintf(monitoringOutputFormatKey, outputName)
	return transpiler.NewRuleList(
		transpiler.Copy(monitoringOutputSelector, outputKey),
		transpiler.Rename(fmt.Sprintf("%s.%s", outputsKey, outputName), t),
		transpiler.Filter(monitoringKey, programsKey, outputKey),
	)
}