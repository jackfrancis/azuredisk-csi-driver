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

// Code generated by lister-gen. DO NOT EDIT.

package v1beta1

import (
	v1beta1 "github.com/abhisheksinghbaghel/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"
)

// AzDriverNodeLister helps list AzDriverNodes.
type AzDriverNodeLister interface {
	// List lists all AzDriverNodes in the indexer.
	List(selector labels.Selector) (ret []*v1beta1.AzDriverNode, err error)
	// AzDriverNodes returns an object that can list and get AzDriverNodes.
	AzDriverNodes(namespace string) AzDriverNodeNamespaceLister
	AzDriverNodeListerExpansion
}

// azDriverNodeLister implements the AzDriverNodeLister interface.
type azDriverNodeLister struct {
	indexer cache.Indexer
}

// NewAzDriverNodeLister returns a new AzDriverNodeLister.
func NewAzDriverNodeLister(indexer cache.Indexer) AzDriverNodeLister {
	return &azDriverNodeLister{indexer: indexer}
}

// List lists all AzDriverNodes in the indexer.
func (s *azDriverNodeLister) List(selector labels.Selector) (ret []*v1beta1.AzDriverNode, err error) {
	err = cache.ListAll(s.indexer, selector, func(m interface{}) {
		ret = append(ret, m.(*v1beta1.AzDriverNode))
	})
	return ret, err
}

// AzDriverNodes returns an object that can list and get AzDriverNodes.
func (s *azDriverNodeLister) AzDriverNodes(namespace string) AzDriverNodeNamespaceLister {
	return azDriverNodeNamespaceLister{indexer: s.indexer, namespace: namespace}
}

// AzDriverNodeNamespaceLister helps list and get AzDriverNodes.
type AzDriverNodeNamespaceLister interface {
	// List lists all AzDriverNodes in the indexer for a given namespace.
	List(selector labels.Selector) (ret []*v1beta1.AzDriverNode, err error)
	// Get retrieves the AzDriverNode from the indexer for a given namespace and name.
	Get(name string) (*v1beta1.AzDriverNode, error)
	AzDriverNodeNamespaceListerExpansion
}

// azDriverNodeNamespaceLister implements the AzDriverNodeNamespaceLister
// interface.
type azDriverNodeNamespaceLister struct {
	indexer   cache.Indexer
	namespace string
}

// List lists all AzDriverNodes in the indexer for a given namespace.
func (s azDriverNodeNamespaceLister) List(selector labels.Selector) (ret []*v1beta1.AzDriverNode, err error) {
	err = cache.ListAllByNamespace(s.indexer, s.namespace, selector, func(m interface{}) {
		ret = append(ret, m.(*v1beta1.AzDriverNode))
	})
	return ret, err
}

// Get retrieves the AzDriverNode from the indexer for a given namespace and name.
func (s azDriverNodeNamespaceLister) Get(name string) (*v1beta1.AzDriverNode, error) {
	obj, exists, err := s.indexer.GetByKey(s.namespace + "/" + name)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.NewNotFound(v1beta1.Resource("azdrivernode"), name)
	}
	return obj.(*v1beta1.AzDriverNode), nil
}
