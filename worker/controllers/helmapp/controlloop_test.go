package helmapp

import (
	"context"

	rainbondv1alpha1 "github.com/goodrain/rainbond/pkg/apis/rainbond/v1alpha1"
	"github.com/goodrain/rainbond/util"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("ControlLoop", func() {
	var namespace string
	var helmApp *rainbondv1alpha1.HelmApp
	BeforeEach(func() {
		// create namespace
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: util.NewUUID(),
			},
		}
		namespace = ns.Name
		By("create namespace: " + namespace)
		_, err := kubeClient.CoreV1().Namespaces().Create(context.Background(), ns, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())

		helmApp = &rainbondv1alpha1.HelmApp{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "phpmyadmin",
				Namespace: namespace,
				Labels: map[string]string{
					"app": "phpmyadmin",
				},
			},
			Spec: rainbondv1alpha1.HelmAppSpec{
				EID:          "5bfba91b0ead72f612732535ef802217",
				TemplateName: "phpmyadmin",
				Version:      "8.2.0",
				AppStore: &rainbondv1alpha1.HelmAppStore{
					Name: "bitnami",
					URL:  "https://charts.bitnami.com/bitnami",
				},
			},
		}
		By("create helm app: " + helmApp.Name)
		_, err = rainbondClient.RainbondV1alpha1().HelmApps(helmApp.Namespace).Create(context.Background(), helmApp, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		By("delete namespace: " + namespace)
		err := kubeClient.CoreV1().Namespaces().Delete(context.Background(), namespace, metav1.DeleteOptions{})
		Expect(err).NotTo(HaveOccurred())
	})

	Describe("Reconcile", func() {
		Context("HelmApp created", func() {
			It("should fulfill default values", func() {
				watch, err := rainbondClient.RainbondV1alpha1().HelmApps(helmApp.Namespace).Watch(context.Background(), metav1.ListOptions{
					LabelSelector: "app=phpmyadmin",
					Watch:         true,
				})
				Expect(err).NotTo(HaveOccurred())

				By("wait until the default values of the helm app were setup")
				for event := range watch.ResultChan() {
					newHelmApp := event.Object.(*rainbondv1alpha1.HelmApp)
					// wait status
					for _, conditionType := range defaultConditionTypes {
						_, condition := newHelmApp.Status.GetCondition(conditionType)
						if condition == nil {
							break
						}
					}
					if newHelmApp.Status.Phase == "" {
						continue
					}

					// wait spec
					if newHelmApp.Spec.PreStatus == "" {
						continue
					}

					break
				}
			})

			It("should start detecting", func() {
				newHelmApp, err := rainbondClient.RainbondV1alpha1().HelmApps(helmApp.Namespace).Get(context.Background(), helmApp.Name, metav1.GetOptions{})
				Expect(err).NotTo(HaveOccurred())

				Expect(newHelmApp.Status.Phase).NotTo(Equal(rainbondv1alpha1.HelmAppStatusPhaseDetecting))

				By("wait until condition detecting conditions become true")
				watch, err := rainbondClient.RainbondV1alpha1().HelmApps(helmApp.Namespace).Watch(context.Background(), metav1.ListOptions{
					LabelSelector: "app=phpmyadmin",
					Watch:         true,
				})
				Expect(err).NotTo(HaveOccurred())

				conditionTypes := []rainbondv1alpha1.HelmAppConditionType{
					rainbondv1alpha1.HelmAppChartReady,
					rainbondv1alpha1.HelmAppChartParsed,
					rainbondv1alpha1.HelmAppPreInstalled,
				}

				for event := range watch.ResultChan() {
					newHelmApp = event.Object.(*rainbondv1alpha1.HelmApp)
					isFinished := true
					for _, conditionType := range conditionTypes {
						_, condition := newHelmApp.Status.GetCondition(conditionType)
						if condition == nil || condition.Status == corev1.ConditionFalse {
							isFinished = false
							break
						}
					}
					if isFinished {
						break
					}
				}
			})

			It("should start configuring", func() {
				By("wait until phase become configuring")
				err := waitUntilConfiguring(helmApp)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("Install HelmApp", func() {
			It("should ok", func() {
				err := waitUntilConfiguring(helmApp)
				Expect(err).NotTo(HaveOccurred())

				newHelmApp, err := rainbondClient.RainbondV1alpha1().HelmApps(helmApp.Namespace).Get(context.Background(), helmApp.Name, metav1.GetOptions{})
				Expect(err).NotTo(HaveOccurred())

				By("install helm app: " + helmApp.Name)
				newHelmApp.Spec.PreStatus = rainbondv1alpha1.HelmAppPreStatusConfigured
				_, err = rainbondClient.RainbondV1alpha1().HelmApps(helmApp.Namespace).Update(context.Background(), newHelmApp, metav1.UpdateOptions{})
				Expect(err).NotTo(HaveOccurred())

				err = waitUntilInstalled(helmApp)
				Expect(err).NotTo(HaveOccurred())
			})
		})
	})
})

func waitUntilConfiguring(helmApp *rainbondv1alpha1.HelmApp) error {
	newHelmApp, err := waitPhaseUntil(helmApp, rainbondv1alpha1.HelmAppStatusPhaseConfiguring)
	if err != nil {
		return err
	}

	if newHelmApp.Status.Readme == "" ||
		len(newHelmApp.Status.Values) == 0 {
		return errors.New("phase is configuring, but readme and values are empty")
	}
	return nil
}

func waitUntilInstalled(helmApp *rainbondv1alpha1.HelmApp) error {
	_, err := waitPhaseUntil(helmApp, rainbondv1alpha1.HelmAppStatusPhaseInstalled)
	return err
}

func waitPhaseUntil(helmApp *rainbondv1alpha1.HelmApp, phase rainbondv1alpha1.HelmAppStatusPhase) (*rainbondv1alpha1.HelmApp, error) {
	watch, err := rainbondClient.RainbondV1alpha1().HelmApps(helmApp.Namespace).Watch(context.Background(), metav1.ListOptions{
		LabelSelector: "app=phpmyadmin",
		Watch:         true,
	})
	if err != nil {
		return nil, err
	}

	// TODO: timeout
	for event := range watch.ResultChan() {
		newHelmApp := event.Object.(*rainbondv1alpha1.HelmApp)
		if newHelmApp.Status.Phase == phase {
			return newHelmApp, nil
		}
	}

	return nil, nil
}
