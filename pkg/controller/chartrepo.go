package controller

import (
	"github.com/alauda/captain/pkg/helm"
	"github.com/alauda/helm-crds/pkg/apis/app/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog"
)

// updateChartRepoStatus update ChartRepo's status
func (c *Controller) updateChartRepoStatus(cr *v1alpha1.ChartRepo, phase v1alpha1.ChartRepoPhase, reason string) {
	cr = cr.DeepCopy()
	cr.Status.Phase = phase
	cr.Status.Reason = reason

	_, err := c.hrClientSet.AppV1alpha1().ChartRepos(cr.Namespace).Update(cr)
	if err != nil {
		if apierrors.IsConflict(err) {
			klog.Warningf("chartrepo %s update conflict, rerty... ", cr.GetName())
			old, err := c.hrClientSet.AppV1alpha1().ChartRepos(cr.Namespace).Get(cr.GetName(), v1.GetOptions{})
			if err != nil {
				klog.Error("chartrepo update-get error:", err)
			} else {
				cr.ResourceVersion = old.ResourceVersion
				_, err := c.hrClientSet.AppV1alpha1().ChartRepos(cr.Namespace).Update(cr)
				if err != nil {
					klog.Error("chartrepo update-update error:", err)
				}
			}
		} else {
			klog.Error("update chartrepo error: ", err)
		}
	}
}

// syncChartRepo sync ChartRepo to helm repo store
func (c *Controller) syncChartRepo(obj interface{}) {

	cr := obj.(*v1alpha1.ChartRepo)

	var username string
	var password string

	if cr.Spec.Secret != nil {
		ns := cr.Spec.Secret.Namespace
		if ns == "" {
			ns = cr.Namespace
		}
		secret, err := c.kubeClient.CoreV1().Secrets(ns).Get(cr.Spec.Secret.Name, v1.GetOptions{})
		if err != nil {
			c.updateChartRepoStatus(cr, v1alpha1.ChartRepoFailed, err.Error())
			klog.Error("get secret for chartrepo error: ", err)
			return
		}
		data := secret.Data
		username = string(data["username"])
		password = string(data["password"])

	}

	if err := helm.AddBasicAuthRepository(cr.GetName(), cr.Spec.URL, username, password); err != nil {
		c.updateChartRepoStatus(cr, v1alpha1.ChartRepoFailed, err.Error())
		return
	}

	c.updateChartRepoStatus(cr, v1alpha1.ChartRepoSynced, "")
	klog.Info("synced chartrepo: ", cr.GetName())
	return

}

func(c *Controller) createCharts() error {

}
