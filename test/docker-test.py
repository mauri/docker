#! /usr/bin/env python
import unittest
import subprocess


class DockerTests(unittest.TestCase):
    def test_docker_run(self):
        out = run("docker run -t debian:latest ls")
        self.assertIn("bin", out)

    def test_docker_ip_address_assignment(self):
        out = run("docker run -t --net=routed --ip-address=10.1.2.3 debian:latest ip route")
        self.assertIn("default dev eth0  scope link", out)

    def test_docker_ip_default_route(self):
        for ip in ("ip-address", "ip"):
            out = run("docker run -t --net=routed --{}=10.1.2.3 debian:latest ip addr show dev eth0".format(ip))
            self.assertIn("inet 10.1.2.3/32 scope global eth0", out)
        
    def test_docker_container_communication(self):
        # launch a server
        run("docker run -d --name=test_docker_server --net=routed --ip=10.1.1.1 "
            + "mauri/ubuntu-netcat /bin/sh -c 'echo foobarzaa |nc -l -q 0 -p 9999'")
        # launch a client
        out = run("docker run -ti --net=routed --ip=10.1.1.2 mauri/ubuntu-netcat /bin/sh -c 'nc 10.1.1.1 9999'")
        # remove the server
        run("docker rm -f test_docker_server")
        self.assertIn("foobarzaa", out)

    def test_docker_ingress_rules(self):
        # launch a server
        run("docker run -d --name=test_docker_server --net=routed --ip=10.1.1.1 "
            + "--label io.docker.network.endpoint.ingressAllowed=10.1.1.3 "
            + "mauri/ubuntu-netcat /bin/sh -c 'echo foobarzaa |nc -l -q 0 -p 9999'")
        # launch a client that gets a connection refused
        out = run("docker run -ti --net=routed --ip=10.1.1.2 mauri/ubuntu-netcat /bin/sh -c 'nc 10.1.1.1 9999 || true'")
        # launch a client that gets the output
        out2 = run("docker run -ti --net=routed --ip=10.1.1.3 mauri/ubuntu-netcat /bin/sh -c 'nc 10.1.1.1 9999'")
        # remove the server
        run("docker rm -f test_docker_server")
        self.assertIn("Connection refused", out)
        self.assertIn("foobarzaa", out2)


def run(cmd):
    """
    Executes the given command returning a tuple with the return value and the stdout and stderr.
    :param cmd: the command to run
    :return: a tuple containing the return code and the output of the command execution
    """
    use_shell = isinstance(cmd, basestring)
    out = subprocess.check_output(cmd, stderr=subprocess.STDOUT, shell=use_shell, universal_newlines=True)
    return out


if __name__ == '__main__':
    unittest.main()
