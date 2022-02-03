/*
Copyright 2021 The Kubernetes Authors.

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

package provisioner

import (
	"context"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	testingClient "k8s.io/client-go/testing"
	diskv1alpha2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha2"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/fake"
	azurediskInformers "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/informers/externalversions"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
)

const (
	testResync = time.Duration(1) * time.Second
)

var (
	managedDiskPathRE             = regexp.MustCompile(`(?i).*/subscriptions/(?:.*)/resourceGroups/(?:.*)/providers/Microsoft.Compute/disks/(.+)`)
	defaultVolumeNameWithParam    = "default-volume-name-with-param"
	defaultVolumeNameWithNilParam = "default-volume-name-with-nil-param"
	invalidVolumeNameLength       = "invalid-volume-name-length-with-length-above-sixty-three-characters"
	invalidVolumeNameConvention   = "invalid-volume-name-convention-special-char-%$%"
	invalidDiskURI                = "/subscriptions/12345678-90ab-cedf-1234-567890abcdef/resourceGroupsrandomtext/test-rg/providers/Microsoft.Compute/disks/test-disk"
	testNodeName                  = "test-node-name"
	testNameSpace                 = "test-ns"

	defaultAzVolumeWithParamForComparison = diskv1alpha2.AzVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: defaultVolumeNameWithParam,
		},
		Spec: diskv1alpha2.AzVolumeSpec{
			VolumeName:           defaultVolumeNameWithParam,
			MaxMountReplicaCount: 2,
			VolumeCapability: []diskv1alpha2.VolumeCapability{
				{
					AccessType: diskv1alpha2.VolumeCapabilityAccessMount,
					AccessMode: diskv1alpha2.VolumeCapabilityAccessModeSingleNodeWriter,
				},
			},
			CapacityRange: &diskv1alpha2.CapacityRange{
				RequiredBytes: 8,
				LimitBytes:    10,
			},
			Parameters: map[string]string{"skuname": "testname", "location": "westus2"},
			Secrets:    map[string]string{"test1": "test2"},
			ContentVolumeSource: &diskv1alpha2.ContentVolumeSource{
				ContentSource:   diskv1alpha2.ContentVolumeSourceTypeVolume,
				ContentSourceID: "content-volume-source",
			},
			AccessibilityRequirements: &diskv1alpha2.TopologyRequirement{
				Preferred: []diskv1alpha2.Topology{
					{
						Segments: map[string]string{"region": "R1", "zone": "Z1"},
					},
					{
						Segments: map[string]string{"region": "R2", "zone": "Z2"},
					},
				},
				Requisite: []diskv1alpha2.Topology{
					{
						Segments: map[string]string{"region": "R3", "zone": "Z3"},
					},
				},
			},
		},
		Status: diskv1alpha2.AzVolumeStatus{},
	}

	defaultAzVolumeWithNilParamForComparison = diskv1alpha2.AzVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: defaultVolumeNameWithNilParam,
		},
		Spec: diskv1alpha2.AzVolumeSpec{
			VolumeName:           defaultVolumeNameWithNilParam,
			MaxMountReplicaCount: 1,
			VolumeCapability: []diskv1alpha2.VolumeCapability{
				{
					AccessType: diskv1alpha2.VolumeCapabilityAccessMount,
					AccessMode: diskv1alpha2.VolumeCapabilityAccessModeSingleNodeWriter,
				},
			},
			AccessibilityRequirements: &diskv1alpha2.TopologyRequirement{},
		},
		Status: diskv1alpha2.AzVolumeStatus{},
	}

	defaultTopology = diskv1alpha2.TopologyRequirement{
		Preferred: []diskv1alpha2.Topology{
			{
				Segments: map[string]string{"region": "R1", "zone": "Z1"},
			},
			{
				Segments: map[string]string{"region": "R2", "zone": "Z2"},
			},
		},
		Requisite: []diskv1alpha2.Topology{
			{
				Segments: map[string]string{"region": "R3", "zone": "Z3"},
			},
		},
	}

	successAzVolStatus = diskv1alpha2.AzVolumeStatus{
		Detail: &diskv1alpha2.AzVolumeStatusDetail{
			VolumeID: testDiskURI,
		},
	}

	successAzVAStatus = diskv1alpha2.AzVolumeAttachmentStatus{
		Detail: &diskv1alpha2.AzVolumeAttachmentStatusDetail{
			PublishContext: map[string]string{"test_key": "test_value"},
			Role:           diskv1alpha2.PrimaryRole,
		},
	}
)

func NewTestCrdProvisioner(controller *gomock.Controller) *CrdProvisioner {
	fakeDiskClient := fake.NewSimpleClientset()
	informerFactory := azurediskInformers.NewSharedInformerFactory(fakeDiskClient, testResync)
	return &CrdProvisioner{
		azDiskClient:     fakeDiskClient,
		namespace:        testNameSpace,
		conditionWatcher: newConditionWatcher(context.Background(), fakeDiskClient, informerFactory, testNameSpace),
	}
}

func TestCrdProvisionerCreateVolume(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	provisioner := NewTestCrdProvisioner(mockCtrl)

	tests := []struct {
		description          string
		existingAzVolumes    []diskv1alpha2.AzVolume
		volumeName           string
		definePrependReactor bool
		capacity             *diskv1alpha2.CapacityRange
		capabilities         []diskv1alpha2.VolumeCapability
		parameters           map[string]string
		secrets              map[string]string
		contentSource        *diskv1alpha2.ContentVolumeSource
		topology             *diskv1alpha2.TopologyRequirement
		expectedError        error
	}{
		{
			description:          "[Success] Create an AzVolume CRI with default parameters",
			existingAzVolumes:    nil,
			volumeName:           testDiskName,
			definePrependReactor: true,
			capacity:             &diskv1alpha2.CapacityRange{},
			capabilities: []diskv1alpha2.VolumeCapability{
				{
					AccessType: diskv1alpha2.VolumeCapabilityAccessMount,
					AccessMode: diskv1alpha2.VolumeCapabilityAccessModeSingleNodeWriter,
				},
			},
			parameters:    make(map[string]string),
			secrets:       make(map[string]string),
			contentSource: &diskv1alpha2.ContentVolumeSource{},
			topology:      &diskv1alpha2.TopologyRequirement{},
			expectedError: nil,
		},
		{
			description:          "[Success] Create an AzVolume CRI with specified parameters",
			existingAzVolumes:    nil,
			volumeName:           testDiskName,
			definePrependReactor: true,
			capacity: &diskv1alpha2.CapacityRange{
				RequiredBytes: 2,
				LimitBytes:    2,
			},
			parameters: map[string]string{"location": "westus2"},
			secrets:    map[string]string{"test1": "No secret"},
			contentSource: &diskv1alpha2.ContentVolumeSource{
				ContentSource:   diskv1alpha2.ContentVolumeSourceTypeVolume,
				ContentSourceID: "content-volume-source",
			},
			topology:      &defaultTopology,
			expectedError: nil,
		},
		{
			description:          "[Success] Create an AzVolume CRI with invalid volume name length",
			existingAzVolumes:    nil,
			volumeName:           invalidVolumeNameLength,
			definePrependReactor: true,
			capacity:             &diskv1alpha2.CapacityRange{},
			capabilities: []diskv1alpha2.VolumeCapability{
				{
					AccessType: diskv1alpha2.VolumeCapabilityAccessMount,
					AccessMode: diskv1alpha2.VolumeCapabilityAccessModeSingleNodeWriter,
				},
			},
			parameters:    make(map[string]string),
			secrets:       make(map[string]string),
			contentSource: &diskv1alpha2.ContentVolumeSource{},
			topology:      &diskv1alpha2.TopologyRequirement{},
			expectedError: nil,
		},
		{
			description:          "[Success] Create an AzVolume CRI with volume name not following the conventions",
			existingAzVolumes:    nil,
			volumeName:           invalidVolumeNameConvention,
			definePrependReactor: true,
			capacity:             &diskv1alpha2.CapacityRange{},
			capabilities: []diskv1alpha2.VolumeCapability{
				{
					AccessType: diskv1alpha2.VolumeCapabilityAccessMount,
					AccessMode: diskv1alpha2.VolumeCapabilityAccessModeSingleNodeWriter,
				},
			},
			parameters:    make(map[string]string),
			secrets:       make(map[string]string),
			contentSource: &diskv1alpha2.ContentVolumeSource{},
			topology:      &diskv1alpha2.TopologyRequirement{},
			expectedError: nil,
		},
		{
			description: "[Success] Return no error when AzVolume CRI exists with identical CreateVolume request parameters",
			existingAzVolumes: []diskv1alpha2.AzVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      testDiskName,
						Namespace: provisioner.namespace,
					},
					Spec: diskv1alpha2.AzVolumeSpec{
						VolumeName: testDiskName,
						CapacityRange: &diskv1alpha2.CapacityRange{
							RequiredBytes: 2,
							LimitBytes:    2,
						},
						VolumeCapability: []diskv1alpha2.VolumeCapability{
							{
								AccessType: diskv1alpha2.VolumeCapabilityAccessMount,
								AccessMode: diskv1alpha2.VolumeCapabilityAccessModeSingleNodeWriter,
							},
						},
						ContentVolumeSource: &diskv1alpha2.ContentVolumeSource{
							ContentSource:   diskv1alpha2.ContentVolumeSourceTypeVolume,
							ContentSourceID: "content-volume-source",
						},
						Parameters:                map[string]string{"location": "westus2"},
						Secrets:                   map[string]string{"secret": "not really"},
						AccessibilityRequirements: &defaultTopology,
					},
					Status: diskv1alpha2.AzVolumeStatus{
						Detail: &diskv1alpha2.AzVolumeStatusDetail{
							VolumeID: testDiskURI,
						},
					},
				},
			},
			volumeName:           testDiskName,
			definePrependReactor: true,
			capacity: &diskv1alpha2.CapacityRange{
				RequiredBytes: 2,
				LimitBytes:    2,
			},
			capabilities: []diskv1alpha2.VolumeCapability{
				{
					AccessType: diskv1alpha2.VolumeCapabilityAccessMount,
					AccessMode: diskv1alpha2.VolumeCapabilityAccessModeSingleNodeWriter,
				},
			},
			parameters: map[string]string{"location": "westus2"},
			secrets:    map[string]string{"secret": "not really"},
			contentSource: &diskv1alpha2.ContentVolumeSource{
				ContentSource:   diskv1alpha2.ContentVolumeSourceTypeVolume,
				ContentSourceID: "content-volume-source",
			},
			topology:      &defaultTopology,
			expectedError: nil,
		},
		{
			description: "[Success] Update previous creation error in existing AzVolume CRI when CreateVolume request for same volumeName is passed",
			existingAzVolumes: []diskv1alpha2.AzVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      testDiskName,
						Namespace: provisioner.namespace,
					},
					Spec: diskv1alpha2.AzVolumeSpec{
						VolumeName: testDiskName,
						CapacityRange: &diskv1alpha2.CapacityRange{
							RequiredBytes: 2,
							LimitBytes:    2,
						},
						VolumeCapability: []diskv1alpha2.VolumeCapability{
							{
								AccessType: diskv1alpha2.VolumeCapabilityAccessMount,
								AccessMode: diskv1alpha2.VolumeCapabilityAccessModeSingleNodeWriter,
							},
						},
						ContentVolumeSource: &diskv1alpha2.ContentVolumeSource{
							ContentSource:   diskv1alpha2.ContentVolumeSourceTypeVolume,
							ContentSourceID: "content-volume-source",
						},
						Parameters:                map[string]string{"location": "westus2"},
						Secrets:                   map[string]string{"secret": "not really"},
						AccessibilityRequirements: &defaultTopology,
					},
					Status: diskv1alpha2.AzVolumeStatus{
						Error: &diskv1alpha2.AzError{
							Message: "Test error message here",
						},
					},
				},
			},
			volumeName:           testDiskName,
			definePrependReactor: true,
			capacity: &diskv1alpha2.CapacityRange{
				RequiredBytes: 2,
				LimitBytes:    2,
			},
			capabilities: []diskv1alpha2.VolumeCapability{
				{
					AccessType: diskv1alpha2.VolumeCapabilityAccessMount,
					AccessMode: diskv1alpha2.VolumeCapabilityAccessModeSingleNodeWriter,
				},
			},
			parameters: map[string]string{"location": "westus2"},
			secrets:    map[string]string{"secret": "not really"},
			contentSource: &diskv1alpha2.ContentVolumeSource{
				ContentSource:   diskv1alpha2.ContentVolumeSourceTypeVolume,
				ContentSourceID: "content-volume-source",
			},
			topology:      &defaultTopology,
			expectedError: nil,
		},
		{
			description: "[Failure] Return AlreadyExists error when an AzVolume CRI exists with same volume name but different CreateVolume request parameters",
			existingAzVolumes: []diskv1alpha2.AzVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      testDiskName,
						Namespace: provisioner.namespace,
					},
					Spec: diskv1alpha2.AzVolumeSpec{
						VolumeName: testDiskName,
						VolumeCapability: []diskv1alpha2.VolumeCapability{
							{
								AccessType: diskv1alpha2.VolumeCapabilityAccessBlock,
								AccessMode: diskv1alpha2.VolumeCapabilityAccessModeSingleNodeWriter,
							},
						},
						Parameters: map[string]string{"parameter": "new params"},
					},
					Status: diskv1alpha2.AzVolumeStatus{
						Detail: &diskv1alpha2.AzVolumeStatusDetail{
							VolumeID:      testDiskURI,
							CapacityBytes: 2,
						},
					},
				},
			},
			volumeName:           testDiskName,
			definePrependReactor: false,
			capacity:             &diskv1alpha2.CapacityRange{},
			capabilities: []diskv1alpha2.VolumeCapability{
				{
					AccessType: diskv1alpha2.VolumeCapabilityAccessMount,
					AccessMode: diskv1alpha2.VolumeCapabilityAccessModeSingleNodeWriter,
				},
			},
			parameters:    make(map[string]string),
			secrets:       make(map[string]string),
			contentSource: &diskv1alpha2.ContentVolumeSource{},
			topology:      &diskv1alpha2.TopologyRequirement{},
			expectedError: status.Errorf(codes.AlreadyExists, "Volume with name (%s) already exists with different specifications", testDiskName),
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(tt.description, func(t *testing.T) {
			existingWatcher := provisioner.conditionWatcher
			defer func() { provisioner.conditionWatcher = existingWatcher }()
			defer func() { provisioner.azDiskClient = fake.NewSimpleClientset() }()

			if tt.existingAzVolumes != nil {
				existingList := make([]runtime.Object, len(tt.existingAzVolumes))
				for itr, azVol := range tt.existingAzVolumes {
					azVol := azVol
					existingList[itr] = &azVol
				}
				provisioner.azDiskClient = fake.NewSimpleClientset(existingList...)
			}

			watcherCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			provisioner.conditionWatcher = newConditionWatcher(watcherCtx, provisioner.azDiskClient, provisioner.newInformerFactory(), provisioner.namespace)

			if tt.definePrependReactor {
				// Using the tracker to insert new object or
				// update the existing object as required
				tracker := provisioner.azDiskClient.(*fake.Clientset).Tracker()

				provisioner.azDiskClient.(*fake.Clientset).Fake.PrependReactor(
					"create",
					"azvolumes",
					func(action testingClient.Action) (bool, runtime.Object, error) {
						objCreated := action.(testingClient.CreateAction).GetObject().(*diskv1alpha2.AzVolume)
						objCreated.Status = successAzVolStatus

						var err error
						if action.GetSubresource() == "" {
							err = tracker.Create(action.GetResource(), objCreated, action.GetNamespace())
						} else {
							err = tracker.Update(action.GetResource(), objCreated, action.GetNamespace())
						}

						if err != nil {
							return true, nil, err
						}

						return true, objCreated, nil
					})

				provisioner.azDiskClient.(*fake.Clientset).Fake.PrependReactor(
					"update",
					"azvolumes",
					func(action testingClient.Action) (bool, runtime.Object, error) {
						objCreated := action.(testingClient.UpdateAction).GetObject().(*diskv1alpha2.AzVolume)
						objCreated.Status = successAzVolStatus
						err := tracker.Update(action.GetResource(), objCreated, action.GetNamespace())

						if err != nil {
							return true, nil, err
						}
						return true, objCreated, nil
					})
			}

			output, outputErr := provisioner.CreateVolume(
				context.TODO(),
				tt.volumeName,
				tt.capacity,
				tt.capabilities,
				tt.parameters,
				tt.secrets,
				tt.contentSource,
				tt.topology)

			assert.Equal(t, tt.expectedError, outputErr)
			if outputErr == nil {
				assert.NotNil(t, output)
			}
		})
	}
}

func TestCrdProvisionerDeleteVolume(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	provisioner := NewTestCrdProvisioner(mockCtrl)

	tests := []struct {
		description       string
		existingAzVolumes []diskv1alpha2.AzVolume
		diskURI           string
		secrets           map[string]string
		expectedError     error
	}{
		{
			description: "[Success] Delete an existing AzVolume CRI entry",
			existingAzVolumes: []diskv1alpha2.AzVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      testDiskName,
						Namespace: provisioner.namespace,
					},
					Spec: diskv1alpha2.AzVolumeSpec{
						VolumeName:           testDiskName,
						MaxMountReplicaCount: 2,
						VolumeCapability: []diskv1alpha2.VolumeCapability{
							{
								AccessType: diskv1alpha2.VolumeCapabilityAccessMount,
								AccessMode: diskv1alpha2.VolumeCapabilityAccessModeSingleNodeWriter,
							},
						},
					},
					Status: diskv1alpha2.AzVolumeStatus{
						Detail: &diskv1alpha2.AzVolumeStatusDetail{
							VolumeID: testDiskURI,
						},
					},
				},
			},
			diskURI:       testDiskURI,
			secrets:       nil,
			expectedError: nil,
		},
		{
			description: "[Success] Delete an existing AzVolume CRI entry when secrets is passed",
			existingAzVolumes: []diskv1alpha2.AzVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      testDiskName,
						Namespace: provisioner.namespace,
					},
					Spec: diskv1alpha2.AzVolumeSpec{
						VolumeName:           testDiskName,
						MaxMountReplicaCount: 2,
						VolumeCapability: []diskv1alpha2.VolumeCapability{
							{
								AccessType: diskv1alpha2.VolumeCapabilityAccessMount,
								AccessMode: diskv1alpha2.VolumeCapabilityAccessModeSingleNodeWriter,
							},
						},
					},
					Status: diskv1alpha2.AzVolumeStatus{
						Detail: &diskv1alpha2.AzVolumeStatusDetail{
							VolumeID: testDiskURI,
						},
					},
				},
			},
			diskURI:       testDiskURI,
			secrets:       map[string]string{"secret": "not really"},
			expectedError: nil,
		},
		{
			description:       "[Success] Return no error on invalid Disk URI in the DeleteVolume request",
			existingAzVolumes: nil,
			diskURI:           invalidDiskURI,
			secrets:           nil,
			expectedError:     nil,
		},
		{
			description:       "[Success] Return no error on missing AzVolume CRI for given Disk URI in the DeleteVolume request",
			existingAzVolumes: nil,
			diskURI:           missingDiskURI,
			secrets:           nil,
			expectedError:     nil,
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(test.description, func(t *testing.T) {
			existingClient := provisioner.azDiskClient
			existingWatcher := provisioner.conditionWatcher
			defer func() { provisioner.conditionWatcher = existingWatcher }()
			defer func() { provisioner.azDiskClient = existingClient }()

			if tt.existingAzVolumes != nil {
				existingList := make([]runtime.Object, len(tt.existingAzVolumes))
				for itr, azVol := range tt.existingAzVolumes {
					azVol := azVol
					existingList[itr] = &azVol
				}
				provisioner.azDiskClient = fake.NewSimpleClientset(existingList...)
			}

			watcherCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			provisioner.conditionWatcher = newConditionWatcher(watcherCtx, provisioner.azDiskClient, provisioner.newInformerFactory(), provisioner.namespace)

			actualError := provisioner.DeleteVolume(
				context.TODO(),
				tt.diskURI,
				tt.secrets)

			assert.Equal(t, tt.expectedError, actualError)
		})
	}
}

func TestCrdProvisionerPublishVolume(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	provisioner := NewTestCrdProvisioner(mockCtrl)

	tests := []struct {
		description             string
		existingAzVolAttachment []diskv1alpha2.AzVolumeAttachment
		diskURI                 string
		nodeID                  string
		volumeContext           map[string]string
		definePrependReactor    bool
		expectedError           error
	}{
		{
			description:             "[Success] Create an AzVolumeAttachment CRI for valid diskURI and nodeID",
			existingAzVolAttachment: nil,
			diskURI:                 testDiskURI,
			nodeID:                  testNodeName,
			volumeContext:           make(map[string]string),
			definePrependReactor:    true,
			expectedError:           nil,
		},
		{
			description:             "[Success] Create an AzVolumeAttachment CRI for valid diskURI, nodeID and volumeContext",
			existingAzVolAttachment: nil,
			diskURI:                 testDiskURI,
			nodeID:                  testNodeName,
			volumeContext:           map[string]string{"volume": "context"},
			definePrependReactor:    true,
			expectedError:           nil,
		},
		{
			description: "[Success] Overwrite previous error state in an AzVolumeAttachment CRI",
			existingAzVolAttachment: []diskv1alpha2.AzVolumeAttachment{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: azureutils.GetAzVolumeAttachmentName(testDiskName, testNodeName),
						Labels: map[string]string{
							consts.NodeNameLabel:   testNodeName,
							consts.VolumeNameLabel: testDiskURI,
						},
						Namespace: provisioner.namespace,
					},
					Spec: diskv1alpha2.AzVolumeAttachmentSpec{
						VolumeName:    azureutils.GetAzVolumeAttachmentName(testDiskName, testNodeName),
						VolumeID:      testDiskName,
						NodeName:      testNodeName,
						VolumeContext: make(map[string]string),
						RequestedRole: diskv1alpha2.PrimaryRole,
					},
					Status: diskv1alpha2.AzVolumeAttachmentStatus{
						Error: &diskv1alpha2.AzError{
							Message: "Test error message here",
						},
						State: diskv1alpha2.AttachmentFailed,
					},
				},
			},
			diskURI:              testDiskURI,
			nodeID:               testNodeName,
			volumeContext:        make(map[string]string),
			definePrependReactor: true,
			expectedError:        nil,
		},
		{
			description: "[Success] Return no error when AzVolumeAttachment CRI with Details and PublishContext exists for the diskURI and nodeID",
			existingAzVolAttachment: []diskv1alpha2.AzVolumeAttachment{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: azureutils.GetAzVolumeAttachmentName(testDiskName, testNodeName),
						Labels: map[string]string{
							consts.NodeNameLabel:   testNodeName,
							consts.VolumeNameLabel: testDiskURI,
						},
						Namespace: provisioner.namespace,
					},
					Spec: diskv1alpha2.AzVolumeAttachmentSpec{
						VolumeName:    azureutils.GetAzVolumeAttachmentName(testDiskName, testNodeName),
						VolumeID:      testDiskName,
						NodeName:      testNodeName,
						VolumeContext: make(map[string]string),
						RequestedRole: diskv1alpha2.PrimaryRole,
					},
					Status: diskv1alpha2.AzVolumeAttachmentStatus{
						Detail: &diskv1alpha2.AzVolumeAttachmentStatusDetail{
							Role:           diskv1alpha2.PrimaryRole,
							PublishContext: map[string]string{},
						},
						State: diskv1alpha2.Attached},
				},
			},
			diskURI:              testDiskURI,
			nodeID:               testNodeName,
			volumeContext:        make(map[string]string),
			definePrependReactor: true,
			expectedError:        nil,
		},
		{
			description: "[Success] Update an existing AzVolumeAttachment CRI with no Details and PublishContext for the diskURI and nodeID",
			existingAzVolAttachment: []diskv1alpha2.AzVolumeAttachment{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: azureutils.GetAzVolumeAttachmentName(testDiskName, testNodeName),
						Labels: map[string]string{
							consts.NodeNameLabel:   testNodeName,
							consts.VolumeNameLabel: testDiskURI,
						},
						Namespace: provisioner.namespace,
					},
					Spec: diskv1alpha2.AzVolumeAttachmentSpec{
						VolumeName:    azureutils.GetAzVolumeAttachmentName(testDiskName, testNodeName),
						VolumeID:      testDiskName,
						NodeName:      testNodeName,
						VolumeContext: make(map[string]string),
						RequestedRole: diskv1alpha2.PrimaryRole,
					},
					Status: diskv1alpha2.AzVolumeAttachmentStatus{},
				},
			},
			diskURI:              testDiskURI,
			nodeID:               testNodeName,
			volumeContext:        make(map[string]string),
			definePrependReactor: true,
			expectedError:        nil,
		},
		{
			description:             "[Failure] Return NotFound error when invalid diskURI is passed",
			existingAzVolAttachment: nil,
			diskURI:                 invalidDiskURI,
			nodeID:                  testNodeName,
			volumeContext:           make(map[string]string),
			definePrependReactor:    false,
			expectedError:           status.Errorf(codes.NotFound, fmt.Sprintf("Error finding volume : could not get disk name from %s, correct format: %s", invalidDiskURI, managedDiskPathRE)),
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(test.description, func(t *testing.T) {
			existingWatcher := provisioner.conditionWatcher
			existingClient := provisioner.azDiskClient
			defer func() { provisioner.conditionWatcher = existingWatcher }()
			defer func() { provisioner.azDiskClient = existingClient }()

			if tt.existingAzVolAttachment != nil {
				existingList := make([]runtime.Object, len(tt.existingAzVolAttachment))
				for itr, azVA := range tt.existingAzVolAttachment {
					azVA := azVA
					existingList[itr] = &azVA
				}
				provisioner.azDiskClient = fake.NewSimpleClientset(existingList...)
			}

			watcherCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			provisioner.conditionWatcher = newConditionWatcher(watcherCtx, provisioner.azDiskClient, provisioner.newInformerFactory(), provisioner.namespace)

			if tt.definePrependReactor {
				// Using the tracker to insert new object or
				// update the existing object as required
				tracker := provisioner.azDiskClient.(*fake.Clientset).Tracker()

				provisioner.azDiskClient.(*fake.Clientset).Fake.PrependReactor(
					"create",
					"azvolumeattachments",
					func(action testingClient.Action) (bool, runtime.Object, error) {
						objCreated := action.(testingClient.CreateAction).GetObject().(*diskv1alpha2.AzVolumeAttachment)
						objCreated.Status = successAzVAStatus

						var err error
						if action.GetSubresource() == "" {
							err = tracker.Create(action.GetResource(), objCreated, action.GetNamespace())
						} else {
							err = tracker.Update(action.GetResource(), objCreated, action.GetNamespace())
						}

						if err != nil {
							return true, nil, err
						}

						return true, objCreated, nil
					})

				provisioner.azDiskClient.(*fake.Clientset).Fake.PrependReactor(
					"update",
					"azvolumeattachments",
					func(action testingClient.Action) (bool, runtime.Object, error) {
						objCreated := action.(testingClient.UpdateAction).GetObject().(*diskv1alpha2.AzVolumeAttachment)
						objCreated.Status = successAzVAStatus
						err := tracker.Update(action.GetResource(), objCreated, action.GetNamespace())

						if err != nil {
							return true, nil, err
						}
						return true, objCreated, nil
					})
			}

			output, outputErr := provisioner.PublishVolume(
				context.TODO(),
				tt.diskURI,
				tt.nodeID,
				nil,
				false,
				make(map[string]string),
				tt.volumeContext)

			assert.Equal(t, tt.expectedError, outputErr)
			if outputErr == nil {
				assert.NotNil(t, output)
			}
		})
	}
}

func TestCrdProvisionerUnpublishVolume(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	provisioner := NewTestCrdProvisioner(mockCtrl)

	tests := []struct {
		description             string
		existingAzVolAttachment []diskv1alpha2.AzVolumeAttachment
		diskURI                 string
		nodeID                  string
		secrets                 map[string]string
		expectedError           error
	}{
		{
			description: "[Success] Delete an AzVolumeAttachment CRI for valid diskURI and nodeID",
			existingAzVolAttachment: []diskv1alpha2.AzVolumeAttachment{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: azureutils.GetAzVolumeAttachmentName(testDiskName, testNodeName),
						Labels: map[string]string{
							consts.NodeNameLabel:   testNodeName,
							consts.VolumeNameLabel: testDiskURI,
						},
						Namespace: provisioner.namespace,
					},
					Spec: diskv1alpha2.AzVolumeAttachmentSpec{
						VolumeName:    azureutils.GetAzVolumeAttachmentName(testDiskName, testNodeName),
						VolumeID:      testDiskName,
						NodeName:      testNodeName,
						VolumeContext: nil,
						RequestedRole: diskv1alpha2.PrimaryRole,
					},
					Status: diskv1alpha2.AzVolumeAttachmentStatus{
						Detail: &diskv1alpha2.AzVolumeAttachmentStatusDetail{
							Role:           diskv1alpha2.PrimaryRole,
							PublishContext: map[string]string{},
						},
						State: diskv1alpha2.Attached,
					},
				},
			},
			diskURI:       testDiskURI,
			nodeID:        testNodeName,
			secrets:       nil,
			expectedError: nil,
		},
		{
			description: "[Success] Delete an AzVolumeAttachment CRI for valid diskURI, nodeID and secrets",
			existingAzVolAttachment: []diskv1alpha2.AzVolumeAttachment{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: azureutils.GetAzVolumeAttachmentName(testDiskName, testNodeName),
						Labels: map[string]string{
							consts.NodeNameLabel:   testNodeName,
							consts.VolumeNameLabel: testDiskURI,
						},
						Namespace: provisioner.namespace,
					},
					Spec: diskv1alpha2.AzVolumeAttachmentSpec{
						VolumeName:    azureutils.GetAzVolumeAttachmentName(testDiskName, testNodeName),
						VolumeID:      testDiskName,
						NodeName:      testNodeName,
						VolumeContext: nil,
						RequestedRole: diskv1alpha2.PrimaryRole,
					},
					Status: diskv1alpha2.AzVolumeAttachmentStatus{
						Detail: &diskv1alpha2.AzVolumeAttachmentStatusDetail{
							Role:           diskv1alpha2.PrimaryRole,
							PublishContext: map[string]string{},
						},
						State: diskv1alpha2.Attached,
					},
				},
			},
			diskURI:       testDiskURI,
			nodeID:        testNodeName,
			secrets:       map[string]string{"secret": "not really"},
			expectedError: nil,
		},
		{
			description:             "[Success] Return no error when an AzVolumeAttachment CRI for diskURI and nodeID is not found",
			existingAzVolAttachment: nil,
			diskURI:                 missingDiskURI,
			nodeID:                  testNodeName,
			secrets:                 nil,
			expectedError:           nil,
		},
		{
			description:             "[Failure] Retun NotFound error when invalid diskURI is passed",
			existingAzVolAttachment: nil,
			diskURI:                 invalidDiskURI,
			nodeID:                  testNodeName,
			secrets:                 nil,
			expectedError:           fmt.Errorf("could not get disk name from %s, correct format: %s", invalidDiskURI, managedDiskPathRE),
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(test.description, func(t *testing.T) {
			existingWatcher := provisioner.conditionWatcher
			existingClient := provisioner.azDiskClient
			defer func() { provisioner.conditionWatcher = existingWatcher }()
			defer func() { provisioner.azDiskClient = existingClient }()

			if tt.existingAzVolAttachment != nil {
				existingList := make([]runtime.Object, len(tt.existingAzVolAttachment))
				for itr, azVA := range tt.existingAzVolAttachment {
					azVA := azVA
					existingList[itr] = &azVA
				}
				provisioner.azDiskClient = fake.NewSimpleClientset(existingList...)
			}

			watcherCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			provisioner.conditionWatcher = newConditionWatcher(watcherCtx, provisioner.azDiskClient, provisioner.newInformerFactory(), provisioner.namespace)

			outputErr := provisioner.UnpublishVolume(
				context.TODO(),
				tt.diskURI,
				tt.nodeID,
				tt.secrets)

			assert.Equal(t, tt.expectedError, outputErr)
		})
	}
}

func TestCrdProvisionerExpandVolume(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	provisioner := NewTestCrdProvisioner(mockCtrl)

	tests := []struct {
		description          string
		existingAzVolumes    []diskv1alpha2.AzVolume
		diskURI              string
		capacityRange        *diskv1alpha2.CapacityRange
		secrets              map[string]string
		definePrependReactor bool
		expectedError        error
	}{
		{
			description: "[Success] Update the CapacityBytes for an existing AzVolume CRI with the given diskURI and enw capacity range",
			existingAzVolumes: []diskv1alpha2.AzVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      testDiskName,
						Namespace: provisioner.namespace,
					},
					Spec: diskv1alpha2.AzVolumeSpec{
						VolumeName: testDiskName,
						VolumeCapability: []diskv1alpha2.VolumeCapability{
							{
								AccessType: diskv1alpha2.VolumeCapabilityAccessMount,
								AccessMode: diskv1alpha2.VolumeCapabilityAccessModeSingleNodeWriter,
							},
						},
						CapacityRange: &diskv1alpha2.CapacityRange{
							RequiredBytes: 3,
							LimitBytes:    3,
						},
					},
					Status: diskv1alpha2.AzVolumeStatus{
						Detail: &diskv1alpha2.AzVolumeStatusDetail{
							VolumeID: testDiskURI,
						},
					},
				},
			},
			diskURI: testDiskURI,
			capacityRange: &diskv1alpha2.CapacityRange{
				RequiredBytes: 4,
				LimitBytes:    4,
			},
			secrets:              nil,
			definePrependReactor: true,
			expectedError:        nil,
		},
		{
			description: "[Success] Update the CapacityBytes for an existing AzVolume CRI with the given diskURI, new capacity range and secrets",
			existingAzVolumes: []diskv1alpha2.AzVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      testDiskName,
						Namespace: provisioner.namespace,
					},
					Spec: diskv1alpha2.AzVolumeSpec{
						VolumeName: testDiskName,
						VolumeCapability: []diskv1alpha2.VolumeCapability{
							{
								AccessType: diskv1alpha2.VolumeCapabilityAccessMount,
								AccessMode: diskv1alpha2.VolumeCapabilityAccessModeSingleNodeWriter,
							},
						},
						CapacityRange: &diskv1alpha2.CapacityRange{
							RequiredBytes: 3,
							LimitBytes:    3,
						},
					},
					Status: diskv1alpha2.AzVolumeStatus{
						Detail: &diskv1alpha2.AzVolumeStatusDetail{
							VolumeID: testDiskURI,
						},
					},
				},
			},
			diskURI: testDiskURI,
			capacityRange: &diskv1alpha2.CapacityRange{
				RequiredBytes: 4,
				LimitBytes:    4,
			},
			secrets:              map[string]string{"secret": "not really"},
			definePrependReactor: true,
			expectedError:        nil,
		},
		{
			description:       "[Failure] Return an error when the AzVolume CRI with the given diskURI doesnt exist",
			existingAzVolumes: nil,
			diskURI:           testDiskURI,
			capacityRange: &diskv1alpha2.CapacityRange{
				RequiredBytes: 4,
				LimitBytes:    4,
			},
			secrets:              nil,
			definePrependReactor: false,
			expectedError:        status.Error(codes.Internal, fmt.Sprintf("Failed to retrieve volume id (%s), error: azvolume.disk.csi.azure.com \"%s\" not found", testDiskURI, testDiskName)),
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(test.description, func(t *testing.T) {
			existingWatcher := provisioner.conditionWatcher
			existingClient := provisioner.azDiskClient
			defer func() { provisioner.conditionWatcher = existingWatcher }()
			defer func() { provisioner.azDiskClient = existingClient }()

			if tt.existingAzVolumes != nil {
				existingList := make([]runtime.Object, len(tt.existingAzVolumes))
				for itr, azVol := range tt.existingAzVolumes {
					azVol := azVol
					existingList[itr] = &azVol
				}
				provisioner.azDiskClient = fake.NewSimpleClientset(existingList...)
			}

			watcherCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			provisioner.conditionWatcher = newConditionWatcher(watcherCtx, provisioner.azDiskClient, provisioner.newInformerFactory(), provisioner.namespace)

			if tt.definePrependReactor {
				// Using the tracker to insert new object or
				// update the existing object as required
				tracker := provisioner.azDiskClient.(*fake.Clientset).Tracker()

				provisioner.azDiskClient.(*fake.Clientset).Fake.PrependReactor(
					"update",
					"azvolumes",
					func(action testingClient.Action) (bool, runtime.Object, error) {
						objPresent := action.(testingClient.UpdateAction).GetObject().(*diskv1alpha2.AzVolume)
						objPresent.Status.Detail.CapacityBytes = tt.capacityRange.RequiredBytes

						err := tracker.Update(action.GetResource(), objPresent, action.GetNamespace())
						if err != nil {
							return true, nil, err
						}

						return true, objPresent, nil
					})
			}

			output, outputErr := provisioner.ExpandVolume(
				context.TODO(),
				tt.diskURI,
				tt.capacityRange,
				tt.secrets)

			assert.Equal(t, tt.expectedError, outputErr)
			if outputErr == nil {
				assert.NotNil(t, output)
			}
		})
	}
}

func TestIsAzVolumeSpecSameAsRequestParams(t *testing.T) {
	tests := []struct {
		description          string
		azVolume             diskv1alpha2.AzVolume
		maxMountReplicaCount int
		capacityRange        *diskv1alpha2.CapacityRange
		parameters           map[string]string
		secrets              map[string]string
		volumeContentSource  *diskv1alpha2.ContentVolumeSource
		accessibilityReq     *diskv1alpha2.TopologyRequirement
		expectedOutput       bool
	}{
		{
			description:          "Verify comparison when all the values are identical and non-nil",
			azVolume:             defaultAzVolumeWithParamForComparison,
			maxMountReplicaCount: 2,
			capacityRange: &diskv1alpha2.CapacityRange{
				RequiredBytes: 8,
				LimitBytes:    10,
			},
			parameters: map[string]string{"skuname": "testname", "location": "westus2"},
			secrets:    map[string]string{"test1": "test2"},
			volumeContentSource: &diskv1alpha2.ContentVolumeSource{
				ContentSource:   diskv1alpha2.ContentVolumeSourceTypeVolume,
				ContentSourceID: "content-volume-source",
			},
			accessibilityReq: &defaultTopology,
			expectedOutput:   true,
		},
		{
			description:          "Verify comparison when values are mismatched and non-nil Parameters map",
			azVolume:             defaultAzVolumeWithParamForComparison,
			maxMountReplicaCount: 2,
			capacityRange: &diskv1alpha2.CapacityRange{
				RequiredBytes: 8,
				LimitBytes:    10,
			},
			parameters: map[string]string{"skuname": "testname1", "location": "westus2"},
			secrets:    map[string]string{"test1": "test2"},
			volumeContentSource: &diskv1alpha2.ContentVolumeSource{
				ContentSource:   diskv1alpha2.ContentVolumeSourceTypeVolume,
				ContentSourceID: "content-volume-source",
			},
			accessibilityReq: &defaultTopology,
			expectedOutput:   false,
		},
		{
			description:          "Verify comparison when values are mismatched and non-nil Secrets map",
			azVolume:             defaultAzVolumeWithParamForComparison,
			maxMountReplicaCount: 2,
			capacityRange: &diskv1alpha2.CapacityRange{
				RequiredBytes: 8,
				LimitBytes:    10,
			},
			parameters: map[string]string{"skuname": "testname", "location": "westus2"},
			secrets:    map[string]string{"test1": "test3"},
			volumeContentSource: &diskv1alpha2.ContentVolumeSource{
				ContentSource:   diskv1alpha2.ContentVolumeSourceTypeVolume,
				ContentSourceID: "content-volume-source",
			},
			accessibilityReq: &defaultTopology,
			expectedOutput:   false,
		},
		{
			description:          "Verify comparison when values are mismatched and non-nil ContentVolumeSource object",
			azVolume:             defaultAzVolumeWithParamForComparison,
			maxMountReplicaCount: 2,
			capacityRange: &diskv1alpha2.CapacityRange{
				RequiredBytes: 8,
				LimitBytes:    10,
			},
			parameters: map[string]string{"skuname": "testname", "location": "westus2"},
			secrets:    map[string]string{"test1": "test2"},
			volumeContentSource: &diskv1alpha2.ContentVolumeSource{
				ContentSource:   diskv1alpha2.ContentVolumeSourceTypeSnapshot,
				ContentSourceID: "content-snapshot-source",
			},
			accessibilityReq: &defaultTopology,
			expectedOutput:   false,
		},
		{
			description:          "Verify comparison when values are mismatched for MaxMountReplicaCount value",
			azVolume:             defaultAzVolumeWithParamForComparison,
			maxMountReplicaCount: 4,
			capacityRange: &diskv1alpha2.CapacityRange{
				RequiredBytes: 8,
				LimitBytes:    10,
			},
			parameters: map[string]string{"skuname": "testname", "location": "westus2"},
			secrets:    map[string]string{"test1": "test2"},
			volumeContentSource: &diskv1alpha2.ContentVolumeSource{
				ContentSource:   diskv1alpha2.ContentVolumeSourceTypeVolume,
				ContentSourceID: "content-volume-source",
			},
			accessibilityReq: &defaultTopology,
			expectedOutput:   false,
		},
		{
			description:          "Verify comparison when values are mismatched and non-nil CapacityRange object",
			azVolume:             defaultAzVolumeWithParamForComparison,
			maxMountReplicaCount: 2,
			capacityRange: &diskv1alpha2.CapacityRange{
				RequiredBytes: 9,
				LimitBytes:    10,
			},
			parameters: map[string]string{"skuname": "testname", "location": "westus2"},
			secrets:    map[string]string{"test1": "test2"},
			volumeContentSource: &diskv1alpha2.ContentVolumeSource{
				ContentSource:   diskv1alpha2.ContentVolumeSourceTypeVolume,
				ContentSourceID: "content-volume-source",
			},
			accessibilityReq: &defaultTopology,
			expectedOutput:   false,
		},
		{
			description:          "Verify comparison when values are mismatched and non-nil AccessibilityRequirements object",
			azVolume:             defaultAzVolumeWithParamForComparison,
			maxMountReplicaCount: 2,
			capacityRange: &diskv1alpha2.CapacityRange{
				RequiredBytes: 8,
				LimitBytes:    10,
			},
			parameters: map[string]string{"skuname": "testname", "location": "westus2"},
			secrets:    map[string]string{"test1": "test2"},
			volumeContentSource: &diskv1alpha2.ContentVolumeSource{
				ContentSource:   diskv1alpha2.ContentVolumeSourceTypeVolume,
				ContentSourceID: "content-volume-source",
			},
			accessibilityReq: &diskv1alpha2.TopologyRequirement{
				Preferred: []diskv1alpha2.Topology{
					{
						Segments: map[string]string{"region": "R1", "zone": "Z1"},
					},
					{
						Segments: map[string]string{"region": "R2", "zone": "Z3"},
					},
				},
				Requisite: []diskv1alpha2.Topology{
					{
						Segments: map[string]string{"region": "R3", "zone": "Z2"},
					},
				},
			},
			expectedOutput: false,
		},
		{
			description:          "Verify comparison between empty and nil map objects",
			azVolume:             defaultAzVolumeWithNilParamForComparison,
			maxMountReplicaCount: 1,
			capacityRange:        &diskv1alpha2.CapacityRange{},
			parameters:           map[string]string{},
			secrets:              map[string]string{},
			volumeContentSource:  &diskv1alpha2.ContentVolumeSource{},
			accessibilityReq:     &diskv1alpha2.TopologyRequirement{},
			expectedOutput:       true,
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(test.description, func(t *testing.T) {
			output := isAzVolumeSpecSameAsRequestParams(
				&tt.azVolume,
				tt.maxMountReplicaCount,
				tt.capacityRange,
				tt.parameters,
				tt.secrets,
				tt.volumeContentSource,
				tt.accessibilityReq)

			assert.Equal(t, tt.expectedOutput, output)
		})
	}
}

func (c *CrdProvisioner) newInformerFactory() azurediskInformers.SharedInformerFactory {
	return azurediskInformers.NewSharedInformerFactory(c.azDiskClient, testResync)
}
