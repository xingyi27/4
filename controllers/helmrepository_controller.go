/*
Copyright 2020 The Flux CD contributors.

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

package controllers

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"path"
	"time"

	sourcerv1 "github.com/fluxcd/sourcer/api/v1alpha1"
	"github.com/go-logr/logr"
	"gopkg.in/yaml.v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// HelmRepositoryReconciler reconciles a HelmRepository object
type HelmRepositoryReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=sourcer.fluxcd.io,resources=helmrepositories,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sourcer.fluxcd.io,resources=helmrepositories/status,verbs=get;update;patch

func (r *HelmRepositoryReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	log := r.Log.WithValues("helmrepository", req.NamespacedName)

	var repo sourcerv1.HelmRepository

	if err := r.Get(ctx, req.NamespacedName, &repo); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	readyCondition := sourcerv1.RepositoryCondition{
		Type:   sourcerv1.RepositoryConditionReady,
		Status: corev1.ConditionUnknown,
	}

	if len(repo.Status.Conditions) == 0 {
		log.Info("Starting index download")
		repo.Status.Conditions = []sourcerv1.RepositoryCondition{readyCondition}
		if err := r.Status().Update(ctx, &repo); err != nil {
			log.Error(err, "unable to update HelmRepository status")
			return ctrl.Result{}, err
		}
	} else {
		for _, condition := range repo.Status.Conditions {
			if condition.Type == sourcerv1.RepositoryConditionReady {
				readyCondition = condition
				break
			}
		}
	}

	if err := r.downloadIndex(repo.Spec.Url); err != nil {
		log.Info("Index download error", "error", err.Error())
		readyCondition.Reason = sourcerv1.IndexDownloadFailedReason
		readyCondition.Message = err.Error()
		readyCondition.Status = corev1.ConditionFalse
	} else {
		log.Info("Index download successful")
		readyCondition.Reason = sourcerv1.IndexDownloadSucceedReason
		readyCondition.Message = "Repository is ready"
		readyCondition.Status = corev1.ConditionTrue
	}

	timeNew := metav1.Now()
	readyCondition.LastTransitionTime = &timeNew
	repo.Status.Conditions = []sourcerv1.RepositoryCondition{readyCondition}

	if err := r.Status().Update(ctx, &repo); err != nil {
		log.Error(err, "unable to update HelmRepository status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: repo.Spec.Interval.Duration}, nil
}

func (r *HelmRepositoryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sourcerv1.HelmRepository{}).
		WithEventFilter(RepositoryChangePredicate{}).
		Complete(r)
}

func (r *HelmRepositoryReconciler) downloadIndex(repoUrl string) error {
	parsedURL, err := url.Parse(repoUrl)
	if err != nil {
		return fmt.Errorf("unable to parse repository url %w", err)
	}
	parsedURL.RawPath = path.Join(parsedURL.RawPath, "index.yaml")
	parsedURL.Path = path.Join(parsedURL.Path, "index.yaml")
	indexURL := parsedURL.String()

	res, err := http.DefaultClient.Get(indexURL)
	if err != nil {
		return fmt.Errorf("unable to download repository index %w", err)
	}

	defer res.Body.Close()

	if res.StatusCode > 300 {
		return fmt.Errorf("unable to download repository index, respose status code %v", res.StatusCode)
	}

	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("unable to read repository index %w", err)
	}

	index := struct {
		APIVersion string    `json:"apiVersion"`
		Generated  time.Time `json:"generated"`
	}{}

	if err := yaml.Unmarshal(body, &index); err != nil {
		return fmt.Errorf("unable to unmarshal repository index %w", err)
	}

	return nil
}
