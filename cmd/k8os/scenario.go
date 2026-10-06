package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/pecodaty/k8os/scenario"
	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func scenarioClient() (kubernetes.Interface, error) {
	var config *rest.Config
	var err error
	if path := os.Getenv("KUBECONFIG"); path != "" {
		config, err = clientcmd.BuildConfigFromFlags("", path)
	} else {
		config, err = rest.InClusterConfig()
		if err != nil {
			config, err = clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("load Kubernetes configuration: %w", err)
	}
	return kubernetes.NewForConfig(config)
}

func newScenarioCommand() *cobra.Command {
	var namespace, image, variant string
	root := &cobra.Command{Use: "scenario", Short: "Run a versioned incident in an exclusive namespace"}
	root.PersistentFlags().StringVar(&namespace, "namespace", "", "exclusive scenario namespace")
	root.PersistentFlags().StringVar(&image, "image", "", "instrumented fixture image (required for apply)")
	root.AddCommand(&cobra.Command{Use: "apply upstream-dependency-v1", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := scenarioName(args[0]); err != nil {
			return err
		}
		client, err := scenarioClient()
		if err != nil {
			return err
		}
		if err := (scenario.Run{Namespace: namespace, Image: image}).Apply(cmd.Context(), client); err != nil {
			return err
		}
		return printJSON(cmd, map[string]string{"scenario": args[0], "namespace": namespace, "image": image, "state": "ready"})
	}})
	trigger := &cobra.Command{Use: "trigger upstream-dependency-v1", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := scenarioName(args[0]); err != nil {
			return err
		}
		client, err := scenarioClient()
		if err != nil {
			return err
		}
		result, err := (scenario.Run{Namespace: namespace, Image: image}).Trigger(cmd.Context(), client, variant)
		if err != nil {
			return err
		}
		return printJSON(cmd, result)
	}}
	trigger.Flags().StringVar(&variant, "variant", "bridge", "attempt variant: bridge, no-bridge, or recovered")
	root.AddCommand(trigger)
	root.AddCommand(&cobra.Command{Use: "evidence upstream-dependency-v1", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := scenarioName(args[0]); err != nil {
			return err
		}
		client, err := scenarioClient()
		if err != nil {
			return err
		}
		result, err := (scenario.Run{Namespace: namespace}).Evidence(cmd.Context(), client)
		if err != nil {
			return err
		}
		return printJSON(cmd, result)
	}})
	root.AddCommand(&cobra.Command{Use: "recover upstream-dependency-v1", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := scenarioName(args[0]); err != nil {
			return err
		}
		client, err := scenarioClient()
		if err != nil {
			return err
		}
		if err := (scenario.Run{Namespace: namespace}).Recover(cmd.Context(), client); err != nil {
			return err
		}
		return printJSON(cmd, map[string]string{"scenario": args[0], "namespace": namespace, "state": "recovered"})
	}})
	root.AddCommand(&cobra.Command{Use: "cleanup upstream-dependency-v1", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := scenarioName(args[0]); err != nil {
			return err
		}
		client, err := scenarioClient()
		if err != nil {
			return err
		}
		if err := (scenario.Run{Namespace: namespace}).Cleanup(cmd.Context(), client); err != nil {
			return err
		}
		return printJSON(cmd, map[string]string{"scenario": args[0], "namespace": namespace, "state": "deleting"})
	}})
	return root
}

func scenarioName(name string) error {
	if name != scenario.UpstreamDependencyV1 {
		return fmt.Errorf("unknown scenario %q", name)
	}
	return nil
}

func printJSON(cmd *cobra.Command, value any) error {
	return json.NewEncoder(cmd.OutOrStdout()).Encode(value)
}
