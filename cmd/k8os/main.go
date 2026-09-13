package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/pecodaty/k8os/chaos"
	"github.com/spf13/cobra"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	if err := newRoot().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRoot() *cobra.Command {
	var namespace, mode string
	var labelFlags []string
	root := &cobra.Command{Use: "k8os", SilenceUsage: true, SilenceErrors: true}
	inject := &cobra.Command{Use: "inject", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		m, err := chaos.ParseMode(mode)
		if err != nil {
			return err
		}
		labels := map[string]string{}
		for _, item := range labelFlags {
			parts := strings.SplitN(item, "=", 2)
			if len(parts) != 2 {
				return fmt.Errorf("label must be key=value")
			}
			labels[parts[0]] = parts[1]
		}
		objects, err := chaos.Render(namespace, m, labels)
		if err != nil {
			return err
		}
		config, err := rest.InClusterConfig()
		if err != nil {
			config, err = clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
		}
		if err != nil {
			return fmt.Errorf("load Kubernetes configuration: %w", err)
		}
		client, err := dynamic.NewForConfig(config)
		if err != nil {
			return err
		}
		if err := chaos.Apply(cmd.Context(), client, namespace, objects); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "created %d resources in namespace %s\n", len(objects), namespace)
		return nil
	}}
	inject.Flags().StringVar(&namespace, "k8os-namespace", chaos.DefaultNamespace, "namespace owned by k8os")
	inject.Flags().StringVar(&mode, "mode", string(chaos.Light), "chaos intensity: light, moderate, or hell")
	inject.Flags().StringArrayVar(&labelFlags, "label", nil, "label applied to generated resources (key=value), repeatable")
	root.AddCommand(inject)
	cleanup := &cobra.Command{Use: "cleanup", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		config, err := rest.InClusterConfig()
		if err != nil {
			config, err = clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
		}
		if err != nil {
			return fmt.Errorf("load Kubernetes configuration: %w", err)
		}
		client, err := dynamic.NewForConfig(config)
		if err != nil {
			return err
		}
		count, err := chaos.Cleanup(cmd.Context(), client, namespace)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "deleted %d k8os resources in namespace %s\n", count, namespace)
		return err
	}}
	cleanup.Flags().StringVar(&namespace, "k8os-namespace", chaos.DefaultNamespace, "namespace owned by k8os")
	root.AddCommand(cleanup)
	return root
}
