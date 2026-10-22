MetalLB:
- In L2 mode one node's speaker answers ARP for each LoadBalancer address, and another takes over when that node leaves; the address must be in the nodes' own network. In BGP mode the speakers peer with routers.
- A Service with no address and an event "no available IPs" means its pool is used up. An address that does not answer: check that a speaker runs on a Ready node, its log, and the IPAddressPool and L2Advertisement (or BGPPeer) in metallb-system. Speakers talk to each other on port 7946.
