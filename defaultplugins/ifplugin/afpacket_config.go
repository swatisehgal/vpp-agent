package ifplugin

import (
	"errors"

	govppapi "git.fd.io/govpp.git/api"
	log "github.com/ligato/cn-infra/logging/logrus"
	intf "github.com/ligato/vpp-agent/defaultplugins/ifplugin/model/interfaces"
	"github.com/ligato/vpp-agent/defaultplugins/ifplugin/vppcalls"
)

// AFPacketConfigurator is used by InterfaceConfigurator to execute afpacket-specific management operations.
// Most importantly it needs to ensure that Afpacket interface is create AFTER the associated host interface.
type AFPacketConfigurator struct {
	withLinuxPlugin  bool                       // is linux plugin loaded ?
	afPacketByHostIf map[string]*AfPacketConfig // host interface name -> Af Packet interface configuration
	afPacketByName   map[string]*AfPacketConfig // af packet name -> Af Packet interface configuration
	hostInterfaces   map[string]struct{}        // a set of available host interfaces

	vppCh *govppapi.Channel // govpp channel used by InterfaceConfigurator
}

// AfPacketConfig wraps the proto formatted configuration of an Afpacket interface together with a flag
// that tells if the interface is waiting for a host interface to get created.
type AfPacketConfig struct {
	config  *intf.Interfaces_Interface
	pending bool
}

// Init members of AFPacketConfigurator.
func (plugin *AFPacketConfigurator) Init(vppCh *govppapi.Channel) (err error) {
	plugin.vppCh = vppCh
	//plugin.withLinuxPlugin = linuxplugin.GetIfIndexes() != nil

	plugin.afPacketByHostIf = make(map[string]*AfPacketConfig)
	plugin.afPacketByName = make(map[string]*AfPacketConfig)
	plugin.hostInterfaces = make(map[string]struct{})
	return nil
}

// ConfigureAfPacketInterface creates a new Afpacket interface or marks it as pending if the target host interface doesn't exist yet.
func (plugin *AFPacketConfigurator) ConfigureAfPacketInterface(afpacket *intf.Interfaces_Interface) (swIndex uint32, pending bool, err error) {

	if afpacket.Type != intf.InterfaceType_AF_PACKET_INTERFACE {
		return 0, false, errors.New("Expecting AfPacket interface")
	}

	if plugin.withLinuxPlugin {
		_, hostIfAvail := plugin.hostInterfaces[afpacket.Afpacket.HostIfName]
		if !hostIfAvail {
			plugin.addToCache(afpacket, true)
			return 0, true, nil
		}
	}
	swIdx, err := vppcalls.AddAfPacketInterface(afpacket.Afpacket, plugin.vppCh)
	if err == nil {
		plugin.addToCache(afpacket, false)
	}
	return swIdx, false, err
}

// ModifyAfPacketInterface updates the cache with afpacket configurations and tells InterfaceConfigurator if the interface
// nees to be recreated for the changes to be applied.
func (plugin *AFPacketConfigurator) ModifyAfPacketInterface(newConfig *intf.Interfaces_Interface,
	oldConfig *intf.Interfaces_Interface) (recreate bool, err error) {

	if oldConfig.Type != intf.InterfaceType_AF_PACKET_INTERFACE ||
		newConfig.Type != intf.InterfaceType_AF_PACKET_INTERFACE {
		return false, errors.New("Expecting AfPacket interface")
	}

	afpacket, found := plugin.afPacketByName[oldConfig.Name]
	if !found || afpacket.pending || (newConfig.Afpacket.HostIfName != oldConfig.Afpacket.HostIfName) {
		return true, nil
	}

	// rewrite cached configuration
	plugin.addToCache(newConfig, false)
	return false, nil
}

// DeleteAfPacketInterface removes Afpacket interface from VPP and from the cache.
func (plugin *AFPacketConfigurator) DeleteAfPacketInterface(afpacket *intf.Interfaces_Interface) (err error) {

	if afpacket.Type != intf.InterfaceType_AF_PACKET_INTERFACE {
		return errors.New("Expecting AfPacket interface")
	}

	config, found := plugin.afPacketByName[afpacket.Name]
	if !found || !config.pending {
		err = vppcalls.DeleteAfPacketInterface(afpacket.GetAfpacket(), plugin.vppCh)
	}
	plugin.removeFromCache(afpacket)
	return err
}

// ResolveCreatedLinuxInterface reacts to a newly created Linux interface.
func (plugin *AFPacketConfigurator) ResolveCreatedLinuxInterface(interfaceName string, interfaceIndex uint32) *intf.Interfaces_Interface {
	if !plugin.withLinuxPlugin {
		log.WithField("hostIfName", interfaceName).Warn("Unexpectedly learned about a new Linux interface")
		return nil
	}
	plugin.hostInterfaces[interfaceName] = struct{}{}

	afpacket, found := plugin.afPacketByHostIf[interfaceName]
	if found {
		if afpacket.pending {
			// afpacket is now free to get created
			return afpacket.config
		}
		log.WithFields(log.Fields{"ifName": afpacket.config.Name, "hostIfName": interfaceName}).Warn(
			"Already configured AFPacket interface")
	}
	return nil // nothing to configure
}

// ResolveDeletedLinuxInterface reacts to a removed Linux interface.
func (plugin *AFPacketConfigurator) ResolveDeletedLinuxInterface(interfaceName string) {
	if !plugin.withLinuxPlugin {
		log.WithField("hostIfName", interfaceName).Warn("Unexpectedly learned about removed Linux interface")
		return
	}
	delete(plugin.hostInterfaces, interfaceName)

	afpacket, found := plugin.afPacketByHostIf[interfaceName]
	if found {
		// remove the interface and re-add as pending
		plugin.DeleteAfPacketInterface(afpacket.config)
		plugin.ConfigureAfPacketInterface(afpacket.config)
	}
}

// IsPendingAfPacket returns true if the given config belongs to pending Afpacket interface.
func (plugin *AFPacketConfigurator) IsPendingAfPacket(iface *intf.Interfaces_Interface) (pending bool) {
	afpacket, found := plugin.afPacketByName[iface.Name]
	return found && afpacket.pending
}

func (plugin *AFPacketConfigurator) addToCache(afpacket *intf.Interfaces_Interface, pending bool) {
	config := &AfPacketConfig{config: afpacket, pending: pending}
	plugin.afPacketByHostIf[afpacket.Afpacket.HostIfName] = config
	plugin.afPacketByName[afpacket.Name] = config
	log.Debugf("Afpacket interface with name %v added to cache (hostIf: %s, pending: %t)",
		afpacket.Name, afpacket.Afpacket.HostIfName, pending)
}

func (plugin *AFPacketConfigurator) removeFromCache(afpacket *intf.Interfaces_Interface) {
	delete(plugin.afPacketByName, afpacket.Name)
	delete(plugin.afPacketByHostIf, afpacket.Afpacket.HostIfName)
	log.Debugf("Afpacket interface with name %v removed from cache", afpacket.Name)
}
