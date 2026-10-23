kube-vip:
- One node, chosen by a Lease, holds each virtual address and answers ARP for it (or announces it over BGP). An address that does not answer: check which node holds the Lease and kube-vip's log there.
