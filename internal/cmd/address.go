package cmd

import (
	"errors"
	"flag"
	"fmt"
	"net"
)

// address lists active network interface addresses suitable for the send command.
func (a *application) address(args []string) error {
	flags := flag.NewFlagSet("address", flag.ContinueOnError)
	flags.SetOutput(a.output)
	port := flags.Int("port", 7432, "Port to show in connection examples")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("address accepts no positional arguments")
	}
	if *port < 1 || *port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	for _, network := range interfaces {
		if network.Flags&net.FlagUp == 0 || network.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := network.Addrs()
		if err != nil {
			return err
		}
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err != nil || !ip.IsGlobalUnicast() {
				continue
			}
			_, _ = fmt.Fprintf(a.output, "%s\t%s\t codex-transfer send %s --port %d\n", network.Name, ip, ip, *port)
		}
	}
	_, _ = fmt.Fprintln(a.output, "Choose an IP reachable from the other computer. Allow the TCP port in its firewall.")
	return nil
}
