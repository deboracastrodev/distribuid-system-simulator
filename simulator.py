import random
import time

# --- O SERVIDOR (O SISTEMA DE REGISTRO) ---
db_status_pedidos = {}
buffer_espera = {}

def process_event_logic(order_id, event):
    print(f"⚙️  [SERVER] Processando {order_id} | Seq: {event['seq']} | Tipo: {event['type']}")
    time.sleep(0.05)
    db_status_pedidos[order_id] = event['seq']

def smart_server_event_handler(order_id, event):
    last_seq = db_status_pedidos.get(order_id, 0)
    current_seq = event['seq']

    if current_seq <= last_seq:
        return f"🛡️  [IGNORADO] Seq {current_seq} já passou."
    if current_seq == last_seq + 1:
        process_event_logic(order_id, event)
        check_waiting_room(order_id)
        return "✅ [SUCESSO]"
    if current_seq > last_seq + 1:
        if order_id not in buffer_espera: buffer_espera[order_id] = {}
        buffer_espera[order_id][current_seq] = event
        return f"⏳ [ESPERA] Seq {current_seq} guardada."

def check_waiting_room(order_id):
    if order_id not in buffer_espera: return
    while True:
        next_expected = db_status_pedidos[order_id] + 1
        if next_expected in buffer_espera[order_id]:
            next_event = buffer_espera[order_id].pop(next_expected)
            print(f"🔓 [SERVER] Libertando Seq {next_expected} do buffer!")
            process_event_logic(order_id, next_event)
        else: break

# --- AGENTE 1: O PLANNER (O Cérebro) ---
class AgentPlanner:
    """
    Este agente recebe um desejo e cria o plano técnico (sequência de eventos).
    """
    def create_mission_plan(self, order_id, value):
        print(f"🧠 [PLANNER] Criando plano para Pedido {order_id} no valor de {value}...")
        return [
            {"id": order_id, "seq": 1, "type": "CRIAR_PEDIDO", "data": value},
            {"id": order_id, "seq": 2, "type": "VALIDAR_ESTOQUE", "data": value},
            {"id": order_id, "seq": 3, "type": "PROCESSAR_PAGAMENTO", "data": value},
            {"id": order_id, "seq": 4, "type": "DESPACHAR_SABRE", "data": value},
        ]

# --- AGENTE 2: O EXECUTOR (A Mão de Obra) ---
class AgentExecutor:
    """
    Este agente executa o plano, mas a rede é caótica sob seu comando.
    """
    def execute_with_chaos(self, plan):
        print(f"🏃 [EXECUTOR] Executando plano com 20% de caos na rede...")
        
        # O Executor embaralha o plano para testar o Servidor!
        chaotic_plan = plan.copy()
        random.shuffle(chaotic_plan)
        
        for step in chaotic_plan:
            print(f"📡 [EXECUTOR] Enviando Seq {step['seq']}...", end=" ")
            result = smart_server_event_handler(step['id'], step)
            print(result)

# --- O SISTEMA MULTI-AGENTE EM AÇÃO ---
def multi_agent_dojo():
    print("\n--- [DOJO] Invocando Conselho de Agentes ---")
    
    planner = AgentPlanner()
    executor = AgentExecutor()
    
    # 1. O desejo do usuário
    order_id = "ORD-MASTER-99"
    
    # 2. O Planner cria o roteiro
    plan = planner.create_mission_plan(order_id, "R$ 5.000,00")
    
    # 3. O Executor tenta realizar a obra (com caos)
    executor.execute_with_chaos(plan)

if __name__ == "__main__":
    multi_agent_dojo()
    
    print("\n--- Estado Final do Sistema ---")
    print(f"Status do Pedido: {db_status_pedidos}")
    if not any(buffer_espera.values()):
        print("✨ Missão Cumprida: Agentes e Servidor em harmonia.")
    else:
        print("⚠️  Algo se perdeu no vácuo do espaço.")
