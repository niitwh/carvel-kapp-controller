// Copyright 2022 VMware, Inc.
// SPDX-License-Identifier: Apache-2.0

package sidecarexec

import (
	"fmt"
	"net/rpc"

	"github.com/vmware-tanzu/carvel-kapp-controller/pkg/config"
)

// OSConfigClient communicates over RPC client to configure runtime environment.
type OSConfigClient struct {
	rpcClient *rpc.Client
}

var _ config.OSConfig = OSConfigClient{}

// ApplyCACerts makes OSConfig.ApplyCACerts RPC call.
func (r OSConfigClient) ApplyCACerts(chain string) error {
	err := r.rpcClient.Call("OSConfig.ApplyCACerts", chain, nil)
	if err != nil {
		return fmt.Errorf("Internal run comm: %s", err)
	}
	return nil
}

// ApplyProxy makes OSConfig.ApplyProxy RPC call.
func (r OSConfigClient) ApplyProxy(in config.ProxyOpts) error {
	err := r.rpcClient.Call("OSConfig.ApplyProxy", in, nil)
	if err != nil {
		return fmt.Errorf("Internal run comm: %s", err)
	}
	return nil
}
