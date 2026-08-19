# The LBU family, exactly the shape the surveyed stacks write it (#281).
#
# Three of the five surveyed Outscale stacks (#262) carry this trio — an
# outscale_load_balancer in a Net with security groups and inline tags, an
# outscale_load_balancer_vms attaching machines by id, and an
# outscale_load_balancer_attributes carrying a health check — and in two of
# them it was the only re-plan residue. The blocks below are the ztiac
# two-tier shape with the fixture's own resources substituted, so the whole
# cycle (apply, empty second plan, destroy) proves what those stacks need.
#
# The destroy order matters as much as the apply: the provider deletes the
# attachment (UnlinkLoadBalancerBackendMachines), then the balancer, then polls
# ReadLoadBalancers until the name is gone before it releases the security
# group and the subnet — and the pack refuses to delete either while the
# balancer stands (TestASubnetDoesNotDeleteUnderALoadBalancer,
# TestASecurityGroupDoesNotDeleteUnderALoadBalancer).

resource "outscale_load_balancer" "conformance" {
  load_balancer_name = "conformance-lb"
  listeners {
    backend_port           = 80
    backend_protocol       = "HTTP"
    load_balancer_protocol = "HTTP"
    load_balancer_port     = 80
  }
  subnets            = [outscale_subnet.conformance.subnet_id]
  security_groups    = [outscale_security_group.net_vm.security_group_id]
  load_balancer_type = "internet-facing"
  tags {
    key   = "Name"
    value = "conformance-lb"
  }
}

resource "outscale_load_balancer_vms" "conformance" {
  load_balancer_name = outscale_load_balancer.conformance.load_balancer_name
  backend_vm_ids     = [outscale_vm.conformance.vm_id]
}

resource "outscale_load_balancer_attributes" "conformance" {
  load_balancer_name = outscale_load_balancer.conformance.load_balancer_name
  health_check {
    healthy_threshold   = 2
    check_interval      = 30
    port                = 80
    protocol            = "HTTP"
    path                = "/healthz"
    timeout             = 5
    unhealthy_threshold = 5
  }
}

# The DNS name is what the surveyed stacks feed to templatefile() and
# local_file: it must be a stable, well-formed name, or the second plan drifts.
output "lb_dns_name" {
  value = outscale_load_balancer.conformance.dns_name
}
