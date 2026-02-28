import random
import time

# --- A CLASSE SERVIDOR (Agora temos instâncias!) ---
class Server:
    def __init__(self, name):
        self.name = name
        self.db_status_pedidos = {}
        self.buffer_espera = {}

    def process_event_logic(self, order_id, event):
        print(f"⚙️  [{self.name}] Processando {order_id} | Seq: {event['seq']} | Tipo: {event['type']}")
        self.db_status_pedidos[order_id] = event['seq']

    def handle_event(self, order_id, event):
        last_seq = self.db_status_pedidos.get(order_id, 0)
        current_seq = event['seq']

        if current_seq <= last_seq:
            return f"🛡️  [{self.name}] IGNORADO: Seq {current_seq} já passou."
        
        if current_seq == last_seq + 1:
            self.process_event_logic(order_id, event)
            self.check_waiting_room(order_id)
            return f"✅ [{self.name}] SUCESSO"
        
        if current_seq > last_seq + 1:
            if order_id not in self.buffer_espera: self.buffer_espera[order_id] = {}
            self.buffer_espera[order_id][current_seq] = event
            return f"⏳ [{self.name}] ESPERA: Seq {current_seq} guardada."

    def check_waiting_room(self, order_id):
        if order_id not in self.buffer_espera: return
        while True:
            next_expected = self.db_status_pedidos.get(order_id, 0) + 1
            if next_expected in self.buffer_espera[order_id]:
                next_event = self.buffer_espera[order_id].pop(next_expected)
                print(f"🔓 [{self.name}] LIBERANDO Seq {next_expected} do buffer!")
                self.process_event_logic(order_id, next_event)
            else: break

# --- AGENTE 1: O PLANNER (Mesma lógica) ---
class AgentPlanner:
    def create_mission_plan(self, order_id, value):
        return [
            {"id": order_id, "seq": 1, "type": "CRIAR_PEDIDO", "data": value},
            {"id": order_id, "seq": 2, "type": "VALIDAR_ESTOQUE", "data": value},
            {"id": order_id, "seq": 3, "type": "PROCESSAR_PAGAMENTO", "data": value},
            {"id": order_id, "seq": 4, "type": "DESPACHAR_SABRE", "data": value},
        ]

# --- AGENTE 2: O EXECUTOR (O CAOS DA PARTIÇÃO) ---
class AgentExecutor:
    def execute_with_partition(self, plan, servers):
        print(f"🏃 [EXECUTOR] Enviando eventos sob PARTIÇÃO DE REDE...")
        
        for step in plan:
            # Simula a rede escolhendo um servidor aleatório (Partição/Load Balancer confuso)
            target_server = random.choice(servers)
            print(f"📡 [REDE] Direcionando Seq {step['seq']} para {target_server.name}...", end=" ")
            result = target_server.handle_event(step['id'], step)
            print(result)

# --- O DOJO COM SPLIT-BRAIN ---
def split_brain_dojo():
    print("\n--- [DOJO] Testando o Teorema CAP (Divergência) ---")
    
    server_alpha = Server("ALPHA")
    server_beta = Server("BETA")
    servers = [server_alpha, server_beta]
    
    planner = AgentPlanner()
    executor = AgentExecutor()
    
    order_id = "ORD-SPLIT-77"
    plan = planner.create_mission_plan(order_id, "R$ 10.000,00")
    
    # Executa o plano distribuído entre os servidores
    executor.execute_with_partition(plan, servers)
    
    print("\n--- ESTADO FINAL (A VERDADE DISTRIBUÍDA) ---")
    print(f"📊 Servidor ALPHA: {server_alpha.db_status_pedidos}")
    print(f"📊 Servidor BETA:  {server_beta.db_status_pedidos}")
    
    if server_alpha.db_status_pedidos == server_beta.db_status_pedidos:
        print("\n✨ Milagre! Sincronia absoluta (Sorte de Jedi).")
    else:
        print("\n💥 SPLIT-BRAIN DETECTADO! Os servidores divergem.")
        print("Para o Teorema CAP, você escolheu Disponibilidade (AP), mas perdeu a Consistência.")

if __name__ == "__main__":
    split_brain_dojo()
