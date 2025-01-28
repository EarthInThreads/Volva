from concurrent import futures
import grpc
from proto.helloworld import helloworld_pb2
from proto.helloworld import helloworld_pb2_grpc

class Greeter(helloworld_pb2_grpc.GreeterServicer):
    def SayHello(self, request, context):
        # 构造响应消息
        message = f"Hello, {request.name}!"
        return helloworld_pb2.HelloReply(message=message)

def serve():
    # 创建gRPC服务器
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=10))
    
    # 注册服务
    helloworld_pb2_grpc.add_GreeterServicer_to_server(Greeter(), server)
    
    # 监听端口
    server.add_insecure_port('[::]:50051')
    server.start()
    print("Server started on port 50051")
    server.wait_for_termination()

if __name__ == '__main__':
    serve()