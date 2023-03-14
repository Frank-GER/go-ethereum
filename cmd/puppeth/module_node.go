// Copyright 2017 The go-ethereum Authors
// This file is part of go-ethereum.
//
// go-ethereum is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// go-ethereum is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with go-ethereum. If not, see <http://www.gnu.org/licenses/>.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
)

// nodeDockerfile is the Dockerfile required to run an Ethereum node.
var nodeDockerfile = `
FROM sidhujag/syscoin-core:latest as syscoin-alpine
FROM alpine:latest

ADD genesis.json /genesis.json
{{if .Unlock}}
	ADD signer.json /signer.json
	ADD signer.pass /signer.pass
{{end}}
ENV SYSCOIN_DATA=/home/syscoin/.syscoin
ENV SYSCOIN_PREFIX=/opt/syscoin
COPY --from=syscoin-alpine ${SYSCOIN_PREFIX}/bin/syscoind /usr/local/bin/syscoind
COPY --from=syscoin-alpine ${SYSCOIN_PREFIX}/bin/syscoin-cli /usr/local/bin/syscoin-cli
COPY --from=syscoin-alpine ${SYSCOIN_PREFIX}/bin/sysgeth /usr/local/bin/sysgeth
RUN apk --no-cache add \
  boost-filesystem \
  boost-system \
  boost-thread \
  libevent \
  libzmq \
  su-exec \
  ca-certificates \
  gmp \
  sqlite-dev

RUN \
    {{if .Unlock}}
	echo 'mkdir -p ${SYSCOIN_DATA}/{{if eq .NetworkID 5700}}testnet3/{{end}}geth/keystore/ && cp /signer.json ${SYSCOIN_DATA}/{{if eq .NetworkID 5700}}testnet3/{{end}}geth/keystore/' >> geth.sh && \{{end}}
	echo $'LC_ALL=C syscoind {{if eq .NetworkID 5700}}--testnet --addnode=52.38.76.82{{end}} --datadir=${SYSCOIN_DATA} --disablewallet --gethcommandline=--port={{.Port}} --gethcommandline=--nat=extip:{{.IP}} --gethcommandline=--maxpeers={{.Peers}} {{.LightFlag}} --gethcommandline=--ethstats={{.Ethstats}} {{if .Bootnodes}}--gethcommandline=--bootnodes={{.Bootnodes}}{{end}} {{if .Etherbase}}--gethcommandline=--miner.etherbase={{.Etherbase}} --gethcommandline=--mine --gethcommandline=--miner.threads=1{{end}} {{if .Unlock}}--gethcommandline=--unlock=0 --gethcommandline=--password=/signer.pass --gethcommandline=--mine{{end}} --gethcommandline=--miner.gaslimit={{.GasLimit}} --gethcommandline=--miner.gasprice={{.GasPrice}} --gethcommandline=--exitwhensynced' >> geth.sh && \
	echo $'LC_ALL=C exec syscoind {{if eq .NetworkID 5700}}--testnet --addnode=52.38.76.82{{end}} --datadir=${SYSCOIN_DATA} --disablewallet --gethcommandline=--port={{.Port}} --gethcommandline=--nat=extip:{{.IP}} --gethcommandline=--maxpeers={{.Peers}} {{.LightFlag}} --gethcommandline=--ethstats={{.Ethstats}} {{if .Bootnodes}}--gethcommandline=--bootnodes={{.Bootnodes}}{{end}} {{if .Etherbase}}--gethcommandline=--miner.etherbase={{.Etherbase}} --gethcommandline=--mine --gethcommandline=--miner.threads=1{{end}} {{if .Unlock}}--gethcommandline=--unlock=0 --gethcommandline=--password=/signer.pass --gethcommandline=--mine{{end}} --gethcommandline=--miner.gaslimit={{.GasLimit}} --gethcommandline=--miner.gasprice={{.GasPrice}}' >> geth.sh

ENTRYPOINT ["/bin/sh", "geth.sh"]
`

// nodeComposefile is the docker-compose.yml file required to deploy and maintain
// an Ethereum node (bootnode or miner for now).
var nodeComposefile = `
version: '2'
services:
  {{.Type}}:
    build: .
    image: {{.Network}}/{{.Type}}
    container_name: {{.Network}}_{{.Type}}_1
    ports:
      - "{{.Port}}:{{.Port}}"
      - "{{.SysPort1}}:{{.SysPort1}}"
      - "{{.SysPort2}}:{{.SysPort2}}"
      - "{{.SysPort3}}:{{.SysPort3}}"
      - "{{.Port}}:{{.Port}}/udp"
    volumes:
      - {{.Datadir}}:/home/syscoin/.syscoin{{if .Ethashdir}}
      - {{.Ethashdir}}:/root/.ethash{{end}}
    environment:
      - PORT={{.Port}}/tcp
      - SYSPORT1={{.SysPort1}}/tcp
      - SYSPORT2={{.SysPort2}}/tcp
      - SYSPORT3={{.SysPort3}}/tcp
      - TOTAL_PEERS={{.TotalPeers}}
      - LIGHT_PEERS={{.LightPeers}}
      - STATS_NAME={{.Ethstats}}
      - MINER_NAME={{.Etherbase}}
      - GAS_LIMIT={{.GasLimit}}
      - GAS_PRICE={{.GasPrice}}
    logging:
      driver: "json-file"
      options:
        max-size: "1m"
        max-file: "10"
    restart: always
`

// deployNode deploys a new Ethereum node container to a remote machine via SSH,
// docker and docker-compose. If an instance with the specified network name
// already exists there, it will be overwritten!
func deployNode(client *sshClient, network string, bootnodes []string, config *nodeInfos, nocache bool) ([]byte, error) {
	kind := "sealnode"
	if config.keyJSON == "" && config.etherbase == "" {
		kind = "bootnode"
		bootnodes = make([]string, 0)
	}
	// Generate the content to upload to the server
	workdir := fmt.Sprintf("%d", rand.Int63())
	files := make(map[string][]byte)

	lightFlag := ""
	if config.peersLight > 0 {
		lightFlag = fmt.Sprintf("--gethcommandline=--light.maxpeers=%d --gethcommandline=--light.serve=50", config.peersLight)
	}
	dockerfile := new(bytes.Buffer)
	template.Must(template.New("").Parse(nodeDockerfile)).Execute(dockerfile, map[string]interface{}{
		"NetworkID": config.network,
		"Port":      config.port,
		"IP":        client.address,
		"Peers":     config.peersTotal,
		"LightFlag": lightFlag,
		"Bootnodes": strings.Join(bootnodes, ","),
		"Ethstats":  config.ethstats,
		"Etherbase": config.etherbase,
		"GasLimit":  uint64(1000000 * config.gasLimit),
		"GasPrice":  uint64(config.gasPrice),
		"Unlock":    config.keyJSON != "",
	})
	files[filepath.Join(workdir, "Dockerfile")] = dockerfile.Bytes()

	composefile := new(bytes.Buffer)
	template.Must(template.New("").Parse(nodeComposefile)).Execute(composefile, map[string]interface{}{
		"Type":       kind,
		"Datadir":    config.datadir,
		"Ethashdir":  config.ethashdir,
		"Network":    network,
		"Port":       config.port,
		"SysPort1":   8369,
		"SysPort2":   18369,
		"SysPort3":   18444,
		"TotalPeers": config.peersTotal,
		"Light":      config.peersLight > 0,
		"LightPeers": config.peersLight,
		"Ethstats":   getEthName(config.ethstats),
		"Etherbase":  config.etherbase,
		"GasLimit":   config.gasLimit,
		"GasPrice":   config.gasPrice,
	})
	files[filepath.Join(workdir, "docker-compose.yaml")] = composefile.Bytes()

	files[filepath.Join(workdir, "genesis.json")] = config.genesis
	if config.keyJSON != "" {
		files[filepath.Join(workdir, "signer.json")] = []byte(config.keyJSON)
		files[filepath.Join(workdir, "signer.pass")] = []byte(config.keyPass)
	}
	// Upload the deployment files to the remote server (and clean up afterwards)
	if out, err := client.Upload(files); err != nil {
		return out, err
	}
	defer client.Run("rm -rf " + workdir)

	// Build and deploy the boot or seal node service
	if nocache {
		return nil, client.Stream(fmt.Sprintf("cd %s && docker-compose -p %s build --pull --no-cache && docker-compose -p %s up -d --force-recreate --timeout 60", workdir, network, network))
	}
	return nil, client.Stream(fmt.Sprintf("cd %s && docker-compose -p %s up -d --build --force-recreate --timeout 60", workdir, network))
}

// nodeInfos is returned from a boot or seal node status check to allow reporting
// various configuration parameters.
type nodeInfos struct {
	genesis    []byte
	network    int64
	datadir    string
	ethashdir  string
	ethstats   string
	port       int
	enode      string
	peersTotal int
	peersLight int
	etherbase  string
	keyJSON    string
	keyPass    string
	gasLimit   float64
	gasPrice   float64
}

// Report converts the typed struct into a plain string->string map, containing
// most - but not all - fields for reporting to the user.
func (info *nodeInfos) Report() map[string]string {
	report := map[string]string{
		"Data directory":           info.datadir,
		"Listener port":            strconv.Itoa(info.port),
		"Peer count (all total)":   strconv.Itoa(info.peersTotal),
		"Peer count (light nodes)": strconv.Itoa(info.peersLight),
		"Ethstats username":        info.ethstats,
	}
	if info.gasLimit > 0 {
		// Miner or signer node
		report["Gas price (minimum accepted)"] = fmt.Sprintf("%0.3f GWei", info.gasPrice)
		report["Gas ceil  (target maximum)"] = fmt.Sprintf("%0.3f MGas", info.gasLimit)

		if info.etherbase != "" {
			// Ethash proof-of-work miner
			report["Ethash directory"] = info.ethashdir
			report["Miner account"] = info.etherbase
		}
		if info.keyJSON != "" {
			// Clique proof-of-authority signer
			var key struct {
				Address string `json:"address"`
			}
			if err := json.Unmarshal([]byte(info.keyJSON), &key); err == nil {
				report["Signer account"] = common.HexToAddress(key.Address).Hex()
			} else {
				log.Error("Failed to retrieve signer address", "err", err)
			}
		}
	}
	return report
}

// checkNode does a health-check against a boot or seal node server to verify
// whether it's running, and if yes, whether it's responsive.
func checkNode(client *sshClient, network string, boot bool) (*nodeInfos, error) {
	kind := "bootnode"
	if !boot {
		kind = "sealnode"
	}
	// Inspect a possible bootnode container on the host
	infos, err := inspectContainer(client, fmt.Sprintf("%s_%s_1", network, kind))
	if err != nil {
		return nil, err
	}
	if !infos.running {
		return nil, ErrServiceOffline
	}
	// Resolve a few types from the environmental variables
	totalPeers, _ := strconv.Atoi(infos.envvars["TOTAL_PEERS"])
	lightPeers, _ := strconv.Atoi(infos.envvars["LIGHT_PEERS"])
	gasLimit, _ := strconv.ParseFloat(infos.envvars["GAS_LIMIT"], 64)
	gasPrice, _ := strconv.ParseFloat(infos.envvars["GAS_PRICE"], 64)

	// Container available, retrieve its node ID and its genesis json
	var out []byte

	// The ipc file is not in the standard location, so we need find it, and specify it.
	if out, err = client.Run(fmt.Sprintf("docker exec %s_%s_1 find /home/syscoin/.syscoin -name geth.ipc", network, kind)); err != nil {
		return nil, ErrServiceUnreachable
	}
	ipcLocation := string(bytes.TrimSpace(out))

	if out, err = client.Run(fmt.Sprintf("docker exec %s_%s_1 /usr/local/bin/sysgeth --exec admin.nodeInfo.enode --cache=16 attach "+ipcLocation, network, kind)); err != nil {
		return nil, ErrServiceUnreachable
	}
	enode := bytes.Trim(bytes.TrimSpace(out), "\"")

	if out, err = client.Run(fmt.Sprintf("docker exec %s_%s_1 cat /genesis.json", network, kind)); err != nil {
		return nil, ErrServiceUnreachable
	}
	genesis := bytes.TrimSpace(out)

	keyJSON, keyPass := "", ""
	if out, err = client.Run(fmt.Sprintf("docker exec %s_%s_1 cat /signer.json", network, kind)); err == nil {
		keyJSON = string(bytes.TrimSpace(out))
	}
	if out, err = client.Run(fmt.Sprintf("docker exec %s_%s_1 cat /signer.pass", network, kind)); err == nil {
		keyPass = string(bytes.TrimSpace(out))
	}
	// Run a sanity check to see if the devp2p is reachable
	port := infos.portmap[infos.envvars["PORT"]]
	if err = checkPort(client.server, port); err != nil {
		log.Warn(fmt.Sprintf("%s devp2p port seems unreachable", strings.Title(kind)), "server", client.server, "port", port, "err", err)
	}
	// Assemble and return the useful infos
	stats := &nodeInfos{
		genesis:    genesis,
		datadir:    infos.volumes["/home/syscoin/.syscoin"],
		ethashdir:  infos.volumes["/root/.ethash"],
		port:       port,
		peersTotal: totalPeers,
		peersLight: lightPeers,
		ethstats:   infos.envvars["STATS_NAME"],
		etherbase:  infos.envvars["MINER_NAME"],
		keyJSON:    keyJSON,
		keyPass:    keyPass,
		gasLimit:   gasLimit,
		gasPrice:   gasPrice,
	}
	stats.enode = string(enode)

	return stats, nil
}
