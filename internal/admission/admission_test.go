package admission_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/workflowspec"
)

func req() workflowspec.Resources {
	return workflowspec.Resources{Nodes: 2, Walltime: workflowspec.Duration(time.Hour)}
}

func TestCheckResourcePolicy(t *testing.T) {
	pol := admission.ResourcePolicy{MaxNodes: 4, MaxGPUsPerJob: 4, AllowExclusive: false}
	if d := admission.CheckResourcePolicy(req(), pol); d != nil {
		t.Fatalf("unexpected denial: %v", d)
	}
	r := req()
	r.Nodes = 8
	if d := admission.CheckResourcePolicy(r, pol); d == nil || d.Code != "RESOURCE_LIMIT" {
		t.Fatalf("want RESOURCE_LIMIT, got %v", d)
	}
	r = req()
	r.GPU = &workflowspec.GPURequest{Type: "h100", Count: 8}
	if d := admission.CheckResourcePolicy(r, pol); d == nil {
		t.Fatal("gpu limit not enforced")
	}
	pol.AllowedGPUTypes = []string{"a100"}
	r.GPU.Count = 1
	if d := admission.CheckResourcePolicy(r, pol); d == nil || d.Code != "GPU_TYPE_DENIED" {
		t.Fatalf("want GPU_TYPE_DENIED, got %v", d)
	}
	r = req()
	r.Exclusive = true
	if d := admission.CheckResourcePolicy(r, pol); d == nil || d.Code != "EXCLUSIVE_DENIED" {
		t.Fatalf("want EXCLUSIVE_DENIED, got %v", d)
	}
}

func TestMemoryPerCPUResourcePolicyUsesDeterminablePerNodeCPUCount(t *testing.T) {
	req := workflowspec.Resources{
		Nodes: 2, TasksPerNode: 4, CPUsPerTask: 1, MemoryPerCPUMiB: 512,
	}
	if denial := admission.CheckResourcePolicy(req, admission.ResourcePolicy{MaxMemoryPerNodeMiB: 2048}); denial != nil {
		t.Fatalf("per-node limit should allow exactly 2048 MiB: %v", denial)
	}
	if denial := admission.CheckResourcePolicy(req, admission.ResourcePolicy{MaxMemoryPerNodeMiB: 2047}); denial == nil || denial.Code != "RESOURCE_LIMIT" {
		t.Fatalf("expected memory-per-cpu limit denial, got %v", denial)
	}

	singleNode := workflowspec.Resources{Nodes: 1, CPUsPerTask: 3, MemoryPerCPUMiB: 512}
	if denial := admission.CheckResourcePolicy(singleNode, admission.ResourcePolicy{MaxMemoryPerNodeMiB: 1535}); denial == nil || denial.Code != "RESOURCE_LIMIT" {
		t.Fatalf("single-node CPU count was not applied to memory-per-cpu: %v", denial)
	}

	multinodeTask := workflowspec.Task{
		Multinode: &workflowspec.Multinode{Nodes: 2, Implementation: "generic"},
		Resources: workflowspec.TaskResources{CPU: 2, MemoryPerCPU: "512Mi", Walltime: "5m"},
	}
	multinode, errs := multinodeTask.ResolveResources("resources")
	if len(errs) != 0 {
		t.Fatalf("multinode request resolution failed: %+v", errs)
	}
	if denial := admission.CheckResourcePolicy(multinode, admission.ResourcePolicy{MaxMemoryPerNodeMiB: 1023}); denial == nil || denial.Code != "RESOURCE_LIMIT" {
		t.Fatalf("multinode per-node CPU count was not applied: %v", denial)
	}

	undetermined := workflowspec.Resources{Nodes: 2, Tasks: 8, CPUsPerTask: 2, MemoryPerCPUMiB: 512}
	if denial := admission.CheckResourcePolicy(undetermined, admission.ResourcePolicy{MaxMemoryPerNodeMiB: 1024}); denial != nil {
		t.Fatalf("unknown per-node task distribution should not fabricate a limit denial: %v", denial)
	}
}

func TestCheckEntitlement(t *testing.T) {
	b := admission.Binding{Account: "proj", DefaultPartition: "main",
		AllowedPartitions: []string{"main", "gpu"}, AllowedQoS: []string{"normal"}}
	acct, part, qos, d := admission.CheckEntitlement(b, "", "normal")
	if d != nil || acct != "proj" || part != "main" || qos != "normal" {
		t.Fatalf("got %s %s %s %v", acct, part, qos, d)
	}
	if _, _, _, d := admission.CheckEntitlement(b, "other", ""); d == nil || d.Code != "PARTITION_DENIED" {
		t.Fatalf("want PARTITION_DENIED, got %v", d)
	}
}

func TestCheckAdmission(t *testing.T) {
	caps := validation.ClusterSnapshot{Partitions: []string{"main"},
		GRESTypes: []string{"gpu:h100"}, MaxWalltime: map[string]time.Duration{"main": 2 * time.Hour}}
	if d := admission.CheckAdmission(req(), "main", caps); d != nil {
		t.Fatalf("unexpected denial: %v", d)
	}
	if d := admission.CheckAdmission(req(), "nope", caps); d == nil || d.Code != "NO_PARTITION" {
		t.Fatalf("want NO_PARTITION, got %v", d)
	}
	r := req()
	r.GPU = &workflowspec.GPURequest{Type: "gpu:v999", Count: 1}
	if d := admission.CheckAdmission(r, "main", caps); d == nil || d.Code != "NO_GRES" {
		t.Fatalf("want NO_GRES, got %v", d)
	}
	r = req()
	r.Walltime = workflowspec.Duration(3 * time.Hour)
	if d := admission.CheckAdmission(r, "main", caps); d == nil || d.Code != "WALLTIME_EXCEEDS_PARTITION" {
		t.Fatalf("want WALLTIME_EXCEEDS_PARTITION, got %v", d)
	}
}

func TestBuildFreezes(t *testing.T) {
	spec := admission.ExecutionSpec{
		ID: uuid.New(), TenantID: uuid.New(), TaskName: "t1",
		Payload: admission.PayloadRef{
			ScriptID: uuid.New(), Digest: validation.DigestOf([]byte("x")),
			Language: workflowspec.LanguageBash, Interpreter: admission.InterpreterBash,
		},
	}
	out, d := admission.Build(admission.BuildInput{
		Spec: spec, Request: req(),
		Policy:  admission.ResourcePolicy{MaxNodes: 4},
		Binding: admission.Binding{Account: "a", DefaultPartition: "main"},
		Cluster: validation.ClusterSnapshot{Partitions: []string{"main"}},
	})
	if d != nil {
		t.Fatalf("denied: %v", d)
	}
	if out.Digest == (validation.Digest{}) || out.SchemaVersion != 1 || out.Partition != "main" {
		t.Fatalf("bad spec: %+v", out)
	}
	if out.Resources.WalltimeSeconds != 3600 {
		t.Fatalf("walltime not normalized: %+v", out.Resources)
	}
	// Canonical is deterministic and excludes Digest/AdmittedAt.
	out.AdmittedAt = time.Now()
	a, _ := out.Canonical()
	b, _ := out.Canonical()
	if string(a) != string(b) {
		t.Fatal("canonical form not deterministic")
	}
}

func TestBuildContainerAndMultinode(t *testing.T) {
	base := admission.BuildInput{
		Spec: admission.ExecutionSpec{
			ID: uuid.New(), TenantID: uuid.New(), TaskName: "container-task",
			Payload: admission.PayloadRef{
				ScriptID: uuid.New(), Digest: validation.DigestOf([]byte("x")),
				Language: workflowspec.LanguageBash, Interpreter: admission.InterpreterBash,
			},
		},
		Request: req(), Policy: admission.ResourcePolicy{MaxNodes: 4},
		Binding: admission.Binding{Account: "a", DefaultPartition: "main"},
		Cluster: validation.ClusterSnapshot{Partitions: []string{"main"}},
	}
	image := &workflowspec.Image{URI: "oras://docker.io/example/image.sif"}
	in := base
	in.Image = image
	if _, denial := admission.Build(in); denial == nil || denial.Code != "CONTAINER_RUNTIME_UNAVAILABLE" {
		t.Fatalf("missing container runtime denial = %v", denial)
	}
	in.CPUAffinity = "numa"
	in.ContainerEnv = map[string]string{"PATH": "/usr/bin", "QUOTE": "a'b; rm -rf /"}
	in.Cluster.ContainerRuntime = &validation.ContainerRuntime{Type: "apptainer"}
	blocked := in
	blocked.Cluster.ContainerRuntime = &validation.ContainerRuntime{
		Type: "apptainer", AllowedImagePrefixes: []string{"docker://trusted.example/"},
	}
	if _, denial := admission.Build(blocked); denial == nil || denial.Code != "IMAGE_NOT_ALLOWED" {
		t.Fatalf("image allow-list denial = %v", denial)
	}
	out, denial := admission.Build(in)
	if denial != nil {
		t.Fatalf("apptainer image denied: %v", denial)
	}
	if out.Container == nil || out.Container.Runtime != "apptainer" ||
		out.Container.Image != image.URI || out.Container.Binary != "apptainer" ||
		out.CPUBind != "ldoms" || out.ContainerEnv["QUOTE"] != "a'b; rm -rf /" {
		t.Fatalf("apptainer execution context not frozen: %+v", out)
	}

	in = base
	in.Image = &workflowspec.Image{URI: "docker://docker.io/anderbubble/image:tag"}
	in.Cluster.ContainerRuntime = &validation.ContainerRuntime{Type: "pyxis"}
	out, denial = admission.Build(in)
	if denial != nil || out.Container == nil || out.Container.Image != "docker.io#anderbubble/image:tag" {
		t.Fatalf("pyxis URI conversion: spec=%+v denial=%v", out.Container, denial)
	}

	in.Cluster.ContainerRuntime.RequireDigest = true
	if _, denial = admission.Build(in); denial == nil || denial.Code != "IMAGE_DIGEST_REQUIRED" {
		t.Fatalf("required image digest denial = %v", denial)
	}

	genericTask := workflowspec.Task{
		Image: image, Multinode: &workflowspec.Multinode{Nodes: 2, Implementation: "generic"},
		Resources: workflowspec.TaskResources{CPU: 2, CPUAffinity: "numa", Memory: "1GB", Walltime: "5m"},
	}
	genericRequest, resourceErrors := genericTask.ResolveResources("resources")
	if len(resourceErrors) != 0 {
		t.Fatalf("generic resource resolution: %+v", resourceErrors)
	}
	in = base
	in.Image = genericTask.Image
	in.Multinode = &workflowspec.Multinode{Nodes: 2, ProcsPerNode: genericTask.EffectiveProcsPerNode(), Implementation: "generic"}
	in.Request = genericRequest
	in.CPUAffinity = genericTask.Resources.CPUAffinity
	in.Cluster.ContainerRuntime = &validation.ContainerRuntime{Type: "apptainer"}
	if _, denial = admission.Build(in); denial == nil || denial.Code != "MULTINODE_CONTAINER_UNSUPPORTED" {
		t.Fatalf("generic image without slurm_in_container denial = %v", denial)
	}
	in.Cluster.ContainerRuntime.SlurmInContainer = true
	out, denial = admission.Build(in)
	if denial != nil || out.Multinode == nil || out.Multinode.Nodes != 2 ||
		out.Multinode.SlotsPerNode != 2 || out.Multinode.Implementation != "generic" || out.CPUBind != "ldoms" {
		t.Fatalf("generic multinode spec: %+v denial=%v", out.Multinode, denial)
	}

	mpi := base
	mpi.Multinode = &workflowspec.Multinode{Nodes: 2, ProcsPerNode: 4, Implementation: "openmpi"}
	mpi.Request = workflowspec.Resources{Nodes: 2, Tasks: 8, TasksPerNode: 4,
		CPUsPerTask: 1, Walltime: workflowspec.Duration(time.Hour)}
	mpi.Cluster.ContainerRuntime = &validation.ContainerRuntime{Type: "apptainer", MPIPlugin: "pmix_v5"}
	out, denial = admission.Build(mpi)
	if denial != nil || out.Multinode == nil || out.Multinode.MPIPlugin != "pmix_v5" {
		t.Fatalf("OpenMPI plugin was not frozen: %+v denial=%v", out.Multinode, denial)
	}
}

func TestBuildImagePullSecretFreezesOnlyNonSecretConfiguration(t *testing.T) {
	in := admission.BuildInput{
		Spec: admission.ExecutionSpec{
			ID: uuid.New(), TenantID: uuid.New(), TaskName: "pull",
			Payload: admission.PayloadRef{
				ScriptID: uuid.New(), Digest: validation.DigestOf([]byte("x")),
				Language: workflowspec.LanguageBash, Interpreter: admission.InterpreterBash,
			},
		},
		Request: workflowspec.Resources{
			Nodes: 1, Tasks: 1, CPUsPerTask: 1, Walltime: workflowspec.Duration(time.Hour),
		},
		Policy:  admission.ResourcePolicy{MaxNodes: 2},
		Binding: admission.Binding{Account: "a", DefaultPartition: "main"},
		Cluster: validation.ClusterSnapshot{
			Partitions:       []string{"main"},
			ContainerRuntime: &validation.ContainerRuntime{Type: "pyxis"},
		},
		Image: &workflowspec.Image{
			URI: "docker://registry.example.com/team/app:1.2",
			PullSecret: &workflowspec.ImagePullSecret{
				UsernameSecret: "registry-user", PasswordSecret: "registry-token",
			},
		},
	}
	out, denial := admission.Build(in)
	if denial != nil {
		t.Fatalf("image pull admission denied: %v", denial)
	}
	if out.Container == nil || !out.Container.PullSecret ||
		!out.Container.PullUsernameSecret || out.Container.PullUsername != "" ||
		out.Container.RegistryHost != "registry.example.com" ||
		out.Container.EnrootImage != "docker://registry.example.com#team/app:1.2" {
		t.Fatalf("image pull configuration was not frozen: %+v", out.Container)
	}
	canonical, err := out.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"registry-user", "registry-token", "very-secret"} {
		if strings.Contains(string(canonical), secret) {
			t.Fatalf("secret handle/value %q was frozen in ExecutionSpec: %s", secret, canonical)
		}
	}

	in.Cluster.ContainerRuntime = &validation.ContainerRuntime{Type: "apptainer"}
	in.Image = &workflowspec.Image{
		URI: "oras://registry.example.com/team/app.sif",
		PullSecret: &workflowspec.ImagePullSecret{
			UsernameSecret: "registry-user", PasswordSecret: "registry-token",
		},
	}
	out, denial = admission.Build(in)
	if denial != nil || out.Container == nil || out.Container.Image != in.Image.URI ||
		!out.Container.PullSecret {
		t.Fatalf("Apptainer ORAS pull secret resolution: container=%+v denial=%v", out.Container, denial)
	}

	in.Cluster.ContainerRuntime = &validation.ContainerRuntime{Type: "pyxis"}
	in.Image.URI = "docker://registry.example.com/team/app:1.2"
	in.Image.PullSecret = &workflowspec.ImagePullSecret{
		Username: "robot$ci", PasswordSecret: "registry-token",
	}
	out, denial = admission.Build(in)
	if denial != nil || out.Container == nil ||
		out.Container.PullUsername != "robot$ci" || out.Container.PullUsernameSecret {
		t.Fatalf("literal username was not frozen as non-secret configuration: container=%+v denial=%v", out.Container, denial)
	}

	in.Image.URI = "oras://registry.example.com/team/app.sif"
	if _, denial = admission.Build(in); denial == nil || denial.Code != "PULL_SECRET_INVALID" {
		t.Fatalf("Pyxis oras pull secret denial = %v", denial)
	}
	in.Image.URI = "docker://registry.example.com/team/app:1.2"
	in.Request.Nodes = 2
	if _, denial = admission.Build(in); denial == nil || denial.Code != "PULL_SECRET_MULTINODE_UNSUPPORTED" {
		t.Fatalf("multi-node pull secret denial = %v", denial)
	}
}

func TestMultinodeRuntimeReferencesAreGenericOnly(t *testing.T) {
	spec := admission.ExecutionSpec{
		Payload:   admission.PayloadRef{Digest: validation.DigestOf([]byte("x"))},
		Multinode: &admission.MultinodeSpec{Implementation: "generic"},
		Argv:      []admission.ArgvElement{{Runtime: admission.RuntimeMultinodeHostlist}},
		Environment: admission.EnvSet{Runtime: map[string]string{
			"HOSTS": admission.RuntimeMultinodeHostlistNoSlots,
		}},
	}
	if denial := admission.CheckArgv(spec); denial != nil {
		t.Fatalf("generic multinode runtime references denied: %v", denial)
	}
	spec.Multinode.Implementation = "openmpi"
	if denial := admission.CheckArgv(spec); denial == nil || denial.Code != "ARGV_RUNTIME" {
		t.Fatalf("non-generic multinode runtime accepted: %v", denial)
	}
}

func TestResolveSoftware(t *testing.T) {
	catalog := []validation.SoftwareModule{
		{Name: "gcc", Modules: []string{"gcc"}},
		{Name: "gcc", Version: "13.2", Modules: []string{"gcc/13.2.0"}},
		{Name: "openmpi", Modules: []string{"openmpi/5.0"}},
	}
	got, d := admission.ResolveSoftware([]workflowspec.SoftwareRequirement{
		{Name: "gcc", Version: "13.2"}, {Name: "gcc", Version: "default"},
		{Name: "openmpi", Version: "default"},
	}, catalog)
	if d != nil {
		t.Fatalf("unexpected denial: %v", d)
	}
	want := [][]string{{"gcc/13.2.0"}, {"gcc"}, {"openmpi/5.0"}}
	for i, w := range want {
		if len(got[i].ModuleSpec) != 1 || got[i].ModuleSpec[0] != w[0] {
			t.Fatalf("requirement %d resolved to %v, want %v", i, got[i].ModuleSpec, w)
		}
	}
	got[0].ModuleSpec[0] = "mutated"
	if catalog[1].Modules[0] != "gcc/13.2.0" {
		t.Fatal("resolution aliases the cluster catalog")
	}
	if _, d := admission.ResolveSoftware([]workflowspec.SoftwareRequirement{
		{Name: "cuda", Version: "12.8"}}, catalog); d == nil || d.Code != "SOFTWARE_UNAVAILABLE" {
		t.Fatalf("want SOFTWARE_UNAVAILABLE, got %v", d)
	}
	if got, d := admission.ResolveSoftware(nil, nil); got != nil || d != nil {
		t.Fatalf("empty requirements: got %v, %v", got, d)
	}
}
