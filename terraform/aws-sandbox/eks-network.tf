# The network of the EKS runs. The subnets are public, so a node reaches EKS,
# ECR, and the image registries through the internet gateway, and the
# sandbox needs no NAT gateway, which costs by the hour. A run creates no
# network resource.
data "aws_availability_zones" "available" {
  state = "available"
}

resource "aws_vpc" "eks" {
  cidr_block           = var.eks_vpc_cidr
  enable_dns_support   = true
  enable_dns_hostnames = true

  tags = {
    Name = "cpo-sandbox-eks"
  }
}

resource "aws_internet_gateway" "eks" {
  vpc_id = aws_vpc.eks.id

  tags = {
    Name = "cpo-sandbox-eks"
  }
}

# EKS needs subnets in two zones.
resource "aws_subnet" "eks" {
  count = 2

  vpc_id                  = aws_vpc.eks.id
  cidr_block              = cidrsubnet(var.eks_vpc_cidr, 4, count.index)
  availability_zone       = data.aws_availability_zones.available.names[count.index]
  map_public_ip_on_launch = true

  tags = {
    Name = "cpo-sandbox-eks-${data.aws_availability_zones.available.names[count.index]}"
  }
}

resource "aws_route_table" "eks" {
  vpc_id = aws_vpc.eks.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.eks.id
  }

  tags = {
    Name = "cpo-sandbox-eks"
  }
}

resource "aws_route_table_association" "eks" {
  count = 2

  subnet_id      = aws_subnet.eks[count.index].id
  route_table_id = aws_route_table.eks.id
}

# The node uses the cluster security group of EKS. The default group gets no
# rules, so that nothing uses it by mistake.
resource "aws_default_security_group" "eks" {
  vpc_id = aws_vpc.eks.id
}
