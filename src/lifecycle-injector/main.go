package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"

	log "github.com/sirupsen/logrus"
	admissionv1beta1 "k8s.io/api/admission/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

func main() {
	log.Info("Started server 8")
	http.HandleFunc("/", handler)
	log.Fatal(http.ListenAndServeTLS(":443", "server.crt", "server.key", nil))
}

func handler(w http.ResponseWriter, r *http.Request) {
	var (
		data []byte
		err  error
	)

	if r.Body != nil {
		data, err = ioutil.ReadAll(r.Body)
		if err != nil {
			log.Warnf("Error reading request: %s", err.Error())
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if len(data) == 0 {
		log.Warn("received empty payload")
		return
	}

	response := processReq(data)
	responseJSON, err := json.Marshal(response)
	if err != nil {
		log.Warnf("Error marshaling request: %s", err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if _, err := w.Write(responseJSON); err != nil {
		log.Warnf("Error writing response: %s", err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func processReq(data []byte) *admissionv1beta1.AdmissionReview {
	admissionReview, err := decode(data)
	if err != nil {
		log.Errorf("failed to decode data. Reason: %s", err)
		admissionReview.Response = &admissionv1beta1.AdmissionResponse{
			UID:     admissionReview.Request.UID,
			Allowed: false,
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
		return admissionReview
	}
	log.Infof("received admission review request %s", admissionReview.Request.UID)

	admissionResponse, err := inject(admissionReview.Request)
	if err != nil {
		log.Error("failed to inject hooks. Reason: ", err)
		admissionReview.Response = &admissionv1beta1.AdmissionResponse{
			UID:     admissionReview.Request.UID,
			Allowed: false,
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
		return admissionReview
	}
	admissionReview.Response = admissionResponse

	return admissionReview
}

func decode(data []byte) (*admissionv1beta1.AdmissionReview, error) {
	var admissionReview admissionv1beta1.AdmissionReview
	err := yaml.Unmarshal(data, &admissionReview)
	return &admissionReview, err
}

func inject(request *admissionv1beta1.AdmissionRequest) (*admissionv1beta1.AdmissionResponse, error) {
	admissionResponse := &admissionv1beta1.AdmissionResponse{
		UID:     request.UID,
		Allowed: true,
	}

	patchJSON, err := getPatch(request.Object.Raw)
	if err != nil {
		return nil, err
	}

	//patchJSON := []byte(`[{"op":"add","path":"/spec/containers/0/lifecycle","value":{}},{"op":"add","path":"/spec/containers/0/lifecycle/-","value":{"preStop":{"exec":{"command":["/bin/bash","-c","sleep 10"]}}}}]`)
	//patchJSON := []byte(`[{"op":"add","path":"/spec/containers/0/lifecycle","value":{"preStop":{"exec":{"command":["/bin/bash","-c","sleep 10"]}}}}]`)

	if len(patchJSON) == 0 {
		return admissionResponse, nil
	}

	log.Infof("patch: %s", patchJSON)
	patchType := admissionv1beta1.PatchTypeJSONPatch
	admissionResponse.Patch = patchJSON
	admissionResponse.PatchType = &patchType

	return admissionResponse, nil
}

type jsonPatch struct {
	Op    string            `json:"op"`
	Path  string            `json:"path"`
	Value *corev1.Lifecycle `json:"value"`
}

func getPatch(bytes []byte) ([]byte, error) {
	var pod *corev1.Pod
	if err := yaml.Unmarshal(bytes, &pod); err != nil {
		return nil, err
	}
	log.Infof("pod: %v", pod)

	pre := []int{}
	for j, container := range pod.Spec.Containers {
		log.Infof("container.Name: %s", container.Name)
		// idempotency!
		if container.Lifecycle != nil {
			continue
		}
		if container.Name == "linkerd-proxy" {
			pre = append(pre, j)
		}
	}

	patch := []jsonPatch{}

	for _, i := range pre {
		patch = append(patch,
			jsonPatch{
				Op:   "add",
				Path: fmt.Sprintf("/spec/containers/%d/lifecycle", i),
				Value: &corev1.Lifecycle{
					PreStop: &corev1.Handler{
						Exec: &corev1.ExecAction{
							Command: []string{
								"/bin/bash",
								"-c",
								"sleep 5",
							},
						},
					},
				},
			},
		)
	}

	log.Infof("pre: %#v", pre)
	log.Infof("patch:: %#v", patch)
	if len(pre) == 0 {
		return nil, nil
	}

	b, err := json.Marshal(patch)
	if err != nil {
		return nil, err
	}
	return b, nil
}
