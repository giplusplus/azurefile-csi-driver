/*
Copyright 2020 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package azurefile

import (
	"context"
	"encoding/base64"
	"fmt"
	"github.com/Azure/azure-sdk-for-go/services/storage/mgmt/2019-06-01/storage"
	azure2 "github.com/Azure/go-autorest/autorest/azure"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/legacy-cloud-providers/azure"
	"k8s.io/legacy-cloud-providers/azure/clients/fileclient/mockfileclient"
	"k8s.io/legacy-cloud-providers/azure/clients/storageaccountclient/mockstorageaccountclient"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestCreateVolume(t *testing.T) {
	stdVolCap := []*csi.VolumeCapability{
		{
			AccessType: &csi.VolumeCapability_Mount{
				Mount: &csi.VolumeCapability_MountVolume{},
			},
			AccessMode: &csi.VolumeCapability_AccessMode{
				Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
			},
		},
	}
	stdVolSize := int64(5 * 1024 * 1024 * 1024)
	stdCapRange := &csi.CapacityRange{RequiredBytes: stdVolSize}
	zeroCapRange := &csi.CapacityRange{RequiredBytes: int64(0)}
	lessThanPremCapRange := &csi.CapacityRange{RequiredBytes: int64(1 * 1024 * 1024 * 1024)}

	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: "Controller Capability missing",
			testFunc: func(t *testing.T) {
				req := &csi.CreateVolumeRequest{
					Name:               "random-vol-name",
					CapacityRange:      stdCapRange,
					VolumeCapabilities: stdVolCap,
					Parameters:         nil,
				}

				ctx := context.Background()
				d := NewFakeDriver()

				expectedErr := status.Errorf(codes.InvalidArgument, "CREATE_DELETE_VOLUME")
				_, err := d.CreateVolume(ctx, req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("Unexpected error: %v", err)
				}
			},
		},
		{
			name: "Volume name missing",
			testFunc: func(t *testing.T) {
				req := &csi.CreateVolumeRequest{
					Name:               "",
					CapacityRange:      stdCapRange,
					VolumeCapabilities: stdVolCap,
					Parameters:         nil,
				}

				ctx := context.Background()
				d := NewFakeDriver()
				d.AddControllerServiceCapabilities(
					[]csi.ControllerServiceCapability_RPC_Type{
						csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
					})

				expectedErr := status.Error(codes.InvalidArgument, "CreateVolume Name must be provided")
				_, err := d.CreateVolume(ctx, req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("Unexpected error: %v", err)
				}
			},
		},
		{
			name: "Volume capabilities missing",
			testFunc: func(t *testing.T) {
				req := &csi.CreateVolumeRequest{
					Name:          "random-vol-name",
					CapacityRange: stdCapRange,
					Parameters:    nil,
				}

				ctx := context.Background()
				d := NewFakeDriver()
				d.AddControllerServiceCapabilities(
					[]csi.ControllerServiceCapability_RPC_Type{
						csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
					})

				expectedErr := status.Error(codes.InvalidArgument, "CreateVolume Volume capabilities must be provided")
				_, err := d.CreateVolume(ctx, req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("Unexpected error: %v", err)
				}
			},
		},
		{
			name: "Create file share errors",
			testFunc: func(t *testing.T) {
				name := "baz"
				sku := "sku"
				kind := "StorageV2"
				location := "centralus"
				value := "foo key"
				accounts := []storage.Account{
					{Name: &name, Sku: &storage.Sku{Name: storage.SkuName(sku)}, Kind: storage.Kind(kind), Location: &location},
				}
				keys := storage.AccountListKeysResult{
					Keys: &[]storage.AccountKey{
						{Value: &value},
					},
				}

				req := &csi.CreateVolumeRequest{
					Name:               "random-vol-name",
					VolumeCapabilities: stdVolCap,
					CapacityRange:      zeroCapRange,
					Parameters: map[string]string{
						"skuname":       "premium",
						"resourcegroup": "rg",
					},
				}

				d := NewFakeDriver()
				d.cloud = &azure.Cloud{}

				ctrl := gomock.NewController(t)
				defer ctrl.Finish()

				tests := []struct {
					desc        string
					err         error
					expectedErr error
				}{
					{
						desc:        "Account Not provisioned",
						err:         fmt.Errorf("StorageAccountIsNotProvisioned"),
						expectedErr: fmt.Errorf("error creating azure client: azure: base storage service url required"),
					},
					{
						desc:        "Too many requests",
						err:         fmt.Errorf("TooManyRequests"),
						expectedErr: fmt.Errorf("error creating azure client: azure: base storage service url required"),
					},
					{
						desc:        "Share not found",
						err:         fmt.Errorf("The specified share does not exist"),
						expectedErr: fmt.Errorf("error creating azure client: azure: base storage service url required"),
					},
					{
						desc:        "Unexpected error",
						err:         fmt.Errorf("test error"),
						expectedErr: fmt.Errorf("test error"),
					},
				}

				for _, test := range tests {
					mockFileClient := mockfileclient.NewMockInterface(ctrl)
					d.cloud.FileClient = mockFileClient

					mockStorageAccountsClient := mockstorageaccountclient.NewMockInterface(ctrl)
					d.cloud.StorageAccountClient = mockStorageAccountsClient

					mockFileClient.EXPECT().CreateFileShare(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(test.err).Times(1)
					mockFileClient.EXPECT().CreateFileShare(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
					mockStorageAccountsClient.EXPECT().ListKeys(gomock.Any(), gomock.Any(), gomock.Any()).Return(keys, nil).AnyTimes()
					mockStorageAccountsClient.EXPECT().ListByResourceGroup(gomock.Any(), gomock.Any()).Return(accounts, nil).AnyTimes()
					mockStorageAccountsClient.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

					d.AddControllerServiceCapabilities(
						[]csi.ControllerServiceCapability_RPC_Type{
							csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
						})

					ctx := context.Background()
					_, err := d.CreateVolume(ctx, req)
					if !reflect.DeepEqual(err, test.expectedErr) {
						if !strings.Contains(err.Error(), test.expectedErr.Error()) {
							t.Errorf("Unexpected error: %v", err)
						}

					}
				}
			},
		},
		{
			name: "No valid key, check all params, with less than min premium volume",
			testFunc: func(t *testing.T) {
				name := "baz"
				sku := "sku"
				kind := "StorageV2"
				location := "centralus"
				value := ""
				accounts := []storage.Account{
					{Name: &name, Sku: &storage.Sku{Name: storage.SkuName(sku)}, Kind: storage.Kind(kind), Location: &location},
				}
				keys := storage.AccountListKeysResult{
					Keys: &[]storage.AccountKey{
						{Value: &value},
					},
				}

				allParam := map[string]string{
					"skuname":            "premium",
					"storageaccounttype": "stoacctype",
					"location":           "loc",
					"storageaccount":     "stoacc",
					"resourcegroup":      "rg",
					shareNameField:       "",
					diskNameField:        "diskname",
					fsTypeField:          "fstype",
					storeAccountKeyField: "storeaccountkey",
					secretNamespaceField: "secretnamespace",
					"defaultparam":       "defaultvalue",
				}

				req := &csi.CreateVolumeRequest{
					Name:               "random-vol-name",
					VolumeCapabilities: stdVolCap,
					CapacityRange:      lessThanPremCapRange,
					Parameters:         allParam,
				}

				d := NewFakeDriver()
				d.cloud = &azure.Cloud{}

				ctrl := gomock.NewController(t)
				defer ctrl.Finish()

				mockFileClient := mockfileclient.NewMockInterface(ctrl)
				d.cloud.FileClient = mockFileClient

				mockStorageAccountsClient := mockstorageaccountclient.NewMockInterface(ctrl)
				d.cloud.StorageAccountClient = mockStorageAccountsClient

				mockFileClient.EXPECT().CreateFileShare(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockStorageAccountsClient.EXPECT().ListKeys(gomock.Any(), gomock.Any(), gomock.Any()).Return(keys, nil).AnyTimes()
				mockStorageAccountsClient.EXPECT().ListByResourceGroup(gomock.Any(), gomock.Any()).Return(accounts, nil).AnyTimes()
				mockStorageAccountsClient.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

				d.AddControllerServiceCapabilities(
					[]csi.ControllerServiceCapability_RPC_Type{
						csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
					})

				ctx := context.Background()
				expectedErr := fmt.Errorf("no valid keys")

				_, err := d.CreateVolume(ctx, req)
				if !strings.Contains(err.Error(), expectedErr.Error()) {
					t.Errorf("Unexpected error: %v", err)
				}
			},
		},
		{
			name: "Base storage URL missing",
			testFunc: func(t *testing.T) {
				name := "baz"
				sku := "sku"
				kind := "StorageV2"
				location := "centralus"
				value := "foo key"
				accounts := []storage.Account{
					{Name: &name, Sku: &storage.Sku{Name: storage.SkuName(sku)}, Kind: storage.Kind(kind), Location: &location},
				}
				keys := storage.AccountListKeysResult{
					Keys: &[]storage.AccountKey{
						{Value: &value},
					},
				}

				req := &csi.CreateVolumeRequest{
					Name:               "random-vol-name",
					VolumeCapabilities: stdVolCap,
					CapacityRange:      zeroCapRange,
					Parameters: map[string]string{
						"skuname":       "premium",
						"resourcegroup": "rg",
					},
				}

				d := NewFakeDriver()
				d.cloud = &azure.Cloud{}

				ctrl := gomock.NewController(t)
				defer ctrl.Finish()

				mockFileClient := mockfileclient.NewMockInterface(ctrl)
				d.cloud.FileClient = mockFileClient

				mockStorageAccountsClient := mockstorageaccountclient.NewMockInterface(ctrl)
				d.cloud.StorageAccountClient = mockStorageAccountsClient

				mockFileClient.EXPECT().CreateFileShare(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockStorageAccountsClient.EXPECT().ListKeys(gomock.Any(), gomock.Any(), gomock.Any()).Return(keys, nil).AnyTimes()
				mockStorageAccountsClient.EXPECT().ListByResourceGroup(gomock.Any(), gomock.Any()).Return(accounts, nil).AnyTimes()
				mockStorageAccountsClient.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

				d.AddControllerServiceCapabilities(
					[]csi.ControllerServiceCapability_RPC_Type{
						csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
					})

				ctx := context.Background()
				expectedErr := fmt.Errorf("error creating azure client: azure: base storage service url required")

				_, err := d.CreateVolume(ctx, req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("Unexpected error: %v", err)
				}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}
}

func TestDeleteVolume(t *testing.T) {
	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: "Volume ID missing",
			testFunc: func(t *testing.T) {
				req := &csi.DeleteVolumeRequest{
					Secrets: map[string]string{},
				}

				ctx := context.Background()
				d := NewFakeDriver()

				expectedErr := status.Error(codes.InvalidArgument, "Volume ID missing in request")
				_, err := d.DeleteVolume(ctx, req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("Unexpected error: %v", err)
				}
			},
		},
		{
			name: "Controller capability missing",
			testFunc: func(t *testing.T) {
				req := &csi.DeleteVolumeRequest{
					VolumeId: "vol_1",
					Secrets:  map[string]string{},
				}

				ctx := context.Background()
				d := NewFakeDriver()

				expectedErr := status.Errorf(codes.InvalidArgument, "invalid delete volume request: %v", req)
				_, err := d.DeleteVolume(ctx, req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("Unexpected error: %v", err)
				}
			},
		},
		{
			name: "Failed to get share URL",
			testFunc: func(t *testing.T) {
				req := &csi.DeleteVolumeRequest{
					VolumeId: "vol_1",
					Secrets:  map[string]string{},
				}

				ctx := context.Background()
				d := NewFakeDriver()
				d.AddControllerServiceCapabilities(
					[]csi.ControllerServiceCapability_RPC_Type{
						csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
					})

				_, err := d.DeleteVolume(ctx, req)
				if !reflect.DeepEqual(err, nil) {
					t.Errorf("Unexpected error: %v", err)
				}
			},
		},
		{
			name: "Dial TCP error",
			testFunc: func(t *testing.T) {
				req := &csi.DeleteVolumeRequest{
					VolumeId: "vol_1#f5713de20cde511e8ba4900#",
					Secrets:  map[string]string{},
				}

				value := base64.StdEncoding.EncodeToString([]byte("acc_key"))
				key := storage.AccountListKeysResult{
					Keys: &[]storage.AccountKey{
						{Value: &value},
					},
				}

				ctx := context.Background()
				d := NewFakeDriver()
				d.cloud = &azure.Cloud{}
				d.AddControllerServiceCapabilities(
					[]csi.ControllerServiceCapability_RPC_Type{
						csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
					})

				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockStorageAccountsClient := mockstorageaccountclient.NewMockInterface(ctrl)
				d.cloud.StorageAccountClient = mockStorageAccountsClient
				clientSet := fake.NewSimpleClientset()
				d.cloud.KubeClient = clientSet
				d.cloud.Environment = azure2.Environment{StorageEndpointSuffix: "abc"}
				mockStorageAccountsClient.EXPECT().ListKeys(gomock.Any(), "vol_1", gomock.Any()).Return(key, nil).AnyTimes()

				_, err := d.DeleteVolume(ctx, req)
				assert.Error(t, err)
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}
}

func TestControllerGetCapabilities(t *testing.T) {
	d := NewFakeDriver()
	controlCap := []*csi.ControllerServiceCapability{
		{
			Type: &csi.ControllerServiceCapability_Rpc{},
		},
	}
	d.Cap = controlCap
	req := csi.ControllerGetCapabilitiesRequest{}
	resp, err := d.ControllerGetCapabilities(context.Background(), &req)
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, resp.Capabilities, controlCap)
}

func TestValidateVolumeCapabilities(t *testing.T) {
	d := NewFakeDriver()
	d.cloud = &azure.Cloud{}

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	value := base64.StdEncoding.EncodeToString([]byte("acc_key"))
	key := storage.AccountListKeysResult{
		Keys: &[]storage.AccountKey{
			{Value: &value},
		},
	}
	clientSet := fake.NewSimpleClientset()
	stdVolCap := []*csi.VolumeCapability{
		{
			AccessType: &csi.VolumeCapability_Mount{
				Mount: &csi.VolumeCapability_MountVolume{},
			},
			AccessMode: &csi.VolumeCapability_AccessMode{
				Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
			},
		},
	}

	tests := []struct {
		desc        string
		req         csi.ValidateVolumeCapabilitiesRequest
		expectedErr error
	}{
		{
			desc:        "Volume ID missing",
			req:         csi.ValidateVolumeCapabilitiesRequest{},
			expectedErr: status.Error(codes.InvalidArgument, "Volume ID not provided"),
		},
		{
			desc:        "Volume capabilities missing",
			req:         csi.ValidateVolumeCapabilitiesRequest{VolumeId: "vol_1"},
			expectedErr: status.Error(codes.InvalidArgument, "Volume capabilities not provided"),
		},
		{
			desc: "Volume ID not valid",
			req: csi.ValidateVolumeCapabilitiesRequest{
				VolumeId:           "vol_1",
				VolumeCapabilities: stdVolCap,
			},
			expectedErr: status.Errorf(codes.NotFound, "error getting volume(vol_1) info: error parsing volume id: \"vol_1\", should at least contain two #"),
		},
	}

	for _, test := range tests {
		mockStorageAccountsClient := mockstorageaccountclient.NewMockInterface(ctrl)
		d.cloud.StorageAccountClient = mockStorageAccountsClient
		d.cloud.KubeClient = clientSet
		d.cloud.Environment = azure2.Environment{StorageEndpointSuffix: "abc"}
		mockStorageAccountsClient.EXPECT().ListKeys(gomock.Any(), "", gomock.Any()).Return(key, nil).AnyTimes()

		_, err := d.ValidateVolumeCapabilities(context.Background(), &test.req)
		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("Unexpected error: %v", err)
		}
	}
}

func TestControllerPublishVolume(t *testing.T) {
	d := NewFakeDriver()
	d.cloud = &azure.Cloud{}
	stdVolCap := csi.VolumeCapability{
		AccessType: &csi.VolumeCapability_Mount{
			Mount: &csi.VolumeCapability_MountVolume{},
		},
		AccessMode: &csi.VolumeCapability_AccessMode{
			Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
		},
	}

	tests := []struct {
		desc        string
		req         *csi.ControllerPublishVolumeRequest
		expectedErr error
	}{
		{
			desc:        "Volume ID missing",
			req:         &csi.ControllerPublishVolumeRequest{},
			expectedErr: status.Error(codes.InvalidArgument, "Volume ID not provided"),
		},
		{
			desc: "Volume capability missing",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId: "vol_1",
			},
			expectedErr: status.Error(codes.InvalidArgument, "Volume capability not provided"),
		},
		{
			desc: "Node ID missing",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         "vol_1",
				VolumeCapability: &stdVolCap,
			},
			expectedErr: status.Error(codes.InvalidArgument, "Node ID not provided"),
		},
	}

	for _, test := range tests {
		_, err := d.ControllerPublishVolume(context.Background(), test.req)
		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("Unexpected error: %v", err)
		}
	}
}

func TestControllerUnpublishVolume(t *testing.T) {
	d := NewFakeDriver()
	d.cloud = &azure.Cloud{}
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	value := base64.StdEncoding.EncodeToString([]byte("acc_key"))
	key := storage.AccountListKeysResult{
		Keys: &[]storage.AccountKey{
			{Value: &value},
		},
	}
	clientSet := fake.NewSimpleClientset()

	tests := []struct {
		desc        string
		req         *csi.ControllerUnpublishVolumeRequest
		expectedErr error
	}{
		{
			desc:        "Volume ID missing",
			req:         &csi.ControllerUnpublishVolumeRequest{},
			expectedErr: status.Error(codes.InvalidArgument, "Volume ID not provided"),
		},
		{
			desc: "Node ID missing",
			req: &csi.ControllerUnpublishVolumeRequest{
				VolumeId: "vol_1",
			},
			expectedErr: status.Error(codes.InvalidArgument, "Node ID not provided"),
		},
		{
			desc: "Disk name empty",
			req: &csi.ControllerUnpublishVolumeRequest{
				VolumeId: "vol_1#f5713de20cde511e8ba4900#fileshare#",
				NodeId:   fakeNodeID,
				Secrets:  map[string]string{},
			},
			expectedErr: nil,
		},
	}

	for _, test := range tests {
		mockStorageAccountsClient := mockstorageaccountclient.NewMockInterface(ctrl)
		d.cloud.StorageAccountClient = mockStorageAccountsClient
		d.cloud.KubeClient = clientSet
		d.cloud.Environment = azure2.Environment{StorageEndpointSuffix: "abc"}
		mockStorageAccountsClient.EXPECT().ListKeys(gomock.Any(), "vol_1", gomock.Any()).Return(key, nil).AnyTimes()

		_, err := d.ControllerUnpublishVolume(context.Background(), test.req)
		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("Unexpected error: %v", err)
		}
	}
}

func TestCreateSnapshot(t *testing.T) {

	d := NewFakeDriver()
	d.cloud = &azure.Cloud{}

	tests := []struct {
		desc        string
		req         *csi.CreateSnapshotRequest
		expectedErr error
	}{
		{
			desc:        "Snapshot name missing",
			req:         &csi.CreateSnapshotRequest{},
			expectedErr: status.Error(codes.InvalidArgument, "Snapshot name must be provided"),
		},
		{
			desc: "Source volume ID",
			req: &csi.CreateSnapshotRequest{
				Name: "snapname",
			},
			expectedErr: status.Error(codes.InvalidArgument, "CreateSnapshot Source Volume ID must be provided"),
		},
		{
			desc: "Invalid volume ID",
			req: &csi.CreateSnapshotRequest{
				SourceVolumeId: "vol_1",
				Name:           "snapname",
			},
			expectedErr: status.Errorf(codes.Internal, "failed to check if snapshot(snapname) exists: error parsing volume id: \"vol_1\", should at least contain two #"),
		},
	}

	for _, test := range tests {
		_, err := d.CreateSnapshot(context.Background(), test.req)
		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("Unexpected error: %v", err)
		}
	}
}

func TestDeleteSnapshot(t *testing.T) {
	d := NewFakeDriver()
	d.cloud = &azure.Cloud{}

	validSecret := map[string]string{}
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	value := base64.StdEncoding.EncodeToString([]byte("acc_key"))
	key := storage.AccountListKeysResult{
		Keys: &[]storage.AccountKey{
			{Value: &value},
		},
	}
	clientSet := fake.NewSimpleClientset()

	tests := []struct {
		desc        string
		req         *csi.DeleteSnapshotRequest
		expectedErr error
	}{
		{
			desc:        "Snapshot name missing",
			req:         &csi.DeleteSnapshotRequest{},
			expectedErr: status.Error(codes.InvalidArgument, "Snapshot ID must be provided"),
		},
		{
			desc: "Invalid volume ID",
			req: &csi.DeleteSnapshotRequest{
				SnapshotId: "vol_1#",
			},
			expectedErr: nil,
		},
		{
			desc: "Invalid volume ID for snapshot name",
			req: &csi.DeleteSnapshotRequest{
				SnapshotId: "vol_1##",
				Secrets:    validSecret,
			},
			expectedErr: status.Errorf(codes.Internal, "failed to get snapshot name with (vol_1##): error parsing volume id: \"vol_1##\", should at least contain four #"),
		},
	}

	for _, test := range tests {
		mockStorageAccountsClient := mockstorageaccountclient.NewMockInterface(ctrl)
		d.cloud.StorageAccountClient = mockStorageAccountsClient
		d.cloud.KubeClient = clientSet
		d.cloud.Environment = azure2.Environment{StorageEndpointSuffix: "abc"}
		mockStorageAccountsClient.EXPECT().ListKeys(gomock.Any(), "vol_1", gomock.Any()).Return(key, nil).AnyTimes()

		_, err := d.DeleteSnapshot(context.Background(), test.req)
		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("Unexpected error: %v", err)
		}
	}
}

func TestControllerExpandVolume(t *testing.T) {
	stdVolSize := int64(5 * 1024 * 1024 * 1024)
	stdCapRange := &csi.CapacityRange{RequiredBytes: stdVolSize}

	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: "Volume ID missing",
			testFunc: func(t *testing.T) {
				req := &csi.ControllerExpandVolumeRequest{}

				ctx := context.Background()
				d := NewFakeDriver()

				expectedErr := status.Error(codes.InvalidArgument, "Volume ID missing in request")
				_, err := d.ControllerExpandVolume(ctx, req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("Unexpected error: %v", err)
				}
			},
		},
		{
			name: "Volume Capacity range missing",
			testFunc: func(t *testing.T) {
				req := &csi.ControllerExpandVolumeRequest{
					VolumeId: "vol_1",
				}

				ctx := context.Background()
				d := NewFakeDriver()

				expectedErr := status.Error(codes.InvalidArgument, "volume capacity range missing in request")
				_, err := d.ControllerExpandVolume(ctx, req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("Unexpected error: %v", err)
				}
			},
		},
		{
			name: "Volume capabilities missing",
			testFunc: func(t *testing.T) {
				req := &csi.ControllerExpandVolumeRequest{
					VolumeId:      "vol_1",
					CapacityRange: stdCapRange,
				}

				ctx := context.Background()
				d := NewFakeDriver()

				expectedErr := status.Errorf(codes.InvalidArgument, "invalid expand volume request: volume_id:\"vol_1\" capacity_range:<required_bytes:5368709120 > ")
				_, err := d.ControllerExpandVolume(ctx, req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("Unexpected error: %v", err)
				}
			},
		},
		{
			name: "Invalid Volume ID",
			testFunc: func(t *testing.T) {
				req := &csi.ControllerExpandVolumeRequest{
					VolumeId:      "vol_1",
					CapacityRange: stdCapRange,
				}

				ctx := context.Background()
				d := NewFakeDriver()
				d.AddControllerServiceCapabilities(
					[]csi.ControllerServiceCapability_RPC_Type{
						csi.ControllerServiceCapability_RPC_EXPAND_VOLUME,
					})

				expectedErr := status.Errorf(codes.InvalidArgument, "GetAccountInfo(vol_1) failed with error: error parsing volume id: \"vol_1\", should at least contain two #")
				_, err := d.ControllerExpandVolume(ctx, req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("Unexpected error: %v", err)
				}
			},
		},
		{
			name: "Disk name not empty",
			testFunc: func(t *testing.T) {
				d := NewFakeDriver()
				d.AddControllerServiceCapabilities(
					[]csi.ControllerServiceCapability_RPC_Type{
						csi.ControllerServiceCapability_RPC_EXPAND_VOLUME,
					})
				d.cloud = &azure.Cloud{}

				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				value := base64.StdEncoding.EncodeToString([]byte("acc_key"))
				key := storage.AccountListKeysResult{
					Keys: &[]storage.AccountKey{
						{Value: &value},
					},
				}
				clientSet := fake.NewSimpleClientset()
				req := &csi.ControllerExpandVolumeRequest{
					VolumeId:      "vol_1#f5713de20cde511e8ba4900#filename#diskname#",
					CapacityRange: stdCapRange,
				}

				ctx := context.Background()
				mockStorageAccountsClient := mockstorageaccountclient.NewMockInterface(ctrl)
				d.cloud.StorageAccountClient = mockStorageAccountsClient
				d.cloud.KubeClient = clientSet
				d.cloud.Environment = azure2.Environment{StorageEndpointSuffix: "abc"}
				mockStorageAccountsClient.EXPECT().ListKeys(gomock.Any(), "vol_1", gomock.Any()).Return(key, nil).AnyTimes()

				expectErr := status.Error(codes.Unimplemented, "vhd disk volume(vol_1#f5713de20cde511e8ba4900#filename#diskname#) is not supported on ControllerExpandVolume")
				_, err := d.ControllerExpandVolume(ctx, req)
				if !reflect.DeepEqual(err, expectErr) {
					t.Errorf("Unexpected error: %v", err)
				}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}
}

func TestGetShareURL(t *testing.T) {
	d := NewFakeDriver()
	validSecret := map[string]string{}
	d.cloud = &azure.Cloud{}

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	value := base64.StdEncoding.EncodeToString([]byte("acc_key"))
	key := storage.AccountListKeysResult{
		Keys: &[]storage.AccountKey{
			{Value: &value},
		},
	}
	clientSet := fake.NewSimpleClientset()
	tests := []struct {
		desc           string
		sourceVolumeID string
		expectedErr    error
	}{
		{
			desc:           "Volume ID error",
			sourceVolumeID: "vol_1",
			expectedErr:    fmt.Errorf("error parsing volume id: \"vol_1\", should at least contain two #"),
		},
		{
			desc:           "Valid request",
			sourceVolumeID: "vol_1##",
			expectedErr:    nil,
		},
	}

	for _, test := range tests {
		mockStorageAccountsClient := mockstorageaccountclient.NewMockInterface(ctrl)
		d.cloud.StorageAccountClient = mockStorageAccountsClient
		d.cloud.KubeClient = clientSet
		d.cloud.Environment = azure2.Environment{StorageEndpointSuffix: "abc"}
		mockStorageAccountsClient.EXPECT().ListKeys(gomock.Any(), "vol_1", gomock.Any()).Return(key, nil).AnyTimes()

		_, err := d.getShareURL(test.sourceVolumeID, validSecret)
		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("Unexpected error: %v", err)
		}
	}
}

func TestGetServiceURL(t *testing.T) {
	d := NewFakeDriver()
	validSecret := map[string]string{}
	d.cloud = &azure.Cloud{}

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	value := base64.StdEncoding.EncodeToString([]byte("acc_key"))
	errValue := "acc_key"
	validKey := storage.AccountListKeysResult{
		Keys: &[]storage.AccountKey{
			{Value: &value},
		},
	}
	errKey := storage.AccountListKeysResult{
		Keys: &[]storage.AccountKey{
			{Value: &errValue},
		},
	}
	clientSet := fake.NewSimpleClientset()
	tests := []struct {
		desc           string
		sourceVolumeID string
		key            storage.AccountListKeysResult
		expectedErr    error
	}{
		{
			desc:           "Invalid volume ID",
			sourceVolumeID: "vol_1",
			key:            validKey,
			expectedErr:    fmt.Errorf("error parsing volume id: \"vol_1\", should at least contain two #"),
		},
		{
			desc:           "Invalid Key",
			sourceVolumeID: "vol_1##",
			key:            errKey,
			expectedErr:    base64.CorruptInputError(3),
		},
		{
			desc:           "Invalid URL",
			sourceVolumeID: "vol_1#^f5713de20cde511e8ba4900#",
			key:            validKey,
			expectedErr:    &url.Error{Op: "parse", URL: "https://^f5713de20cde511e8ba4900.file.abc", Err: url.InvalidHostError("^")},
		},
		{
			desc:           "Valid call",
			sourceVolumeID: "vol_1##",
			key:            validKey,
			expectedErr:    nil,
		},
	}

	for _, test := range tests {
		mockStorageAccountsClient := mockstorageaccountclient.NewMockInterface(ctrl)
		d.cloud.StorageAccountClient = mockStorageAccountsClient
		d.cloud.KubeClient = clientSet
		d.cloud.Environment = azure2.Environment{StorageEndpointSuffix: "abc"}
		mockStorageAccountsClient.EXPECT().ListKeys(gomock.Any(), "vol_1", gomock.Any()).Return(test.key, nil).AnyTimes()

		_, _, err := d.getServiceURL(test.sourceVolumeID, validSecret)
		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("Unexpected error: %v", err)
		}
	}
}

func TestSnapshotExists(t *testing.T) {
	d := NewFakeDriver()
	validSecret := map[string]string{}
	d.cloud = &azure.Cloud{}

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	value := base64.StdEncoding.EncodeToString([]byte("acc_key"))

	validKey := storage.AccountListKeysResult{
		Keys: &[]storage.AccountKey{
			{Value: &value},
		},
	}

	clientSet := fake.NewSimpleClientset()
	tests := []struct {
		desc           string
		sourceVolumeID string
		key            storage.AccountListKeysResult
		expectedErr    error
	}{
		{
			desc:           "Invalid volume ID",
			sourceVolumeID: "vol_1",
			key:            validKey,
			expectedErr:    fmt.Errorf("error parsing volume id: \"vol_1\", should at least contain two #"),
		},
	}

	for _, test := range tests {
		mockStorageAccountsClient := mockstorageaccountclient.NewMockInterface(ctrl)
		d.cloud.StorageAccountClient = mockStorageAccountsClient
		d.cloud.KubeClient = clientSet
		d.cloud.Environment = azure2.Environment{StorageEndpointSuffix: "abc"}
		mockStorageAccountsClient.EXPECT().ListKeys(gomock.Any(), "vol_1", gomock.Any()).Return(test.key, nil).AnyTimes()

		_, _, err := d.snapshotExists(context.Background(), test.sourceVolumeID, "sname", validSecret)
		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("Unexpected error: %v", err)
		}
	}
}
