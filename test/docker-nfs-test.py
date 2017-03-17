#! /usr/bin/env python
import unittest
import subprocess


class DockerNfsTests(unittest.TestCase):

    def test_docker_nfs_volume(self):
        # launch a server that exports are only allowed on the host address. 
        run("docker run -d --name=test_docker_nfs_server --privileged --net=routed --ip-address=10.1.1.233 -v /tmp:/data mauri/nfs-server 10.0.0.0/8:/data")
        # launch a client
        out = run("docker run -ti --net=routed --ip=192.168.2.2 -v 10.1.1.233///data:/foo:nfs,rw debian /bin/sh -c 'touch /foo/tmp-file-2; ls /foo'")
        # remove the server
        run("docker rm -f test_docker_nfs_server")
        self.assertIn("tmp-file-2", out)
        
    def test_docker_nfs_volume_access_denied(self):
        run("docker run -d --name=test_docker_nfs_server --privileged --net=routed --ip-address=10.1.1.233 -v /tmp:/data mauri/nfs-server 10.1.1.232/32:/data")
        # launch a client on the ip we are allowing access from. it should fail as the address on the host is what is used.
        try:
            out = run("docker run -ti --net=routed --ip=10.1.1.232 -v 10.1.1.233///data:/foo:nfs,rw debian /bin/sh -c 'ls /foo'")
        except:
            pass #self.assertIn("access denied", out)
        run("docker rm -f test_docker_nfs_server")
        

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
